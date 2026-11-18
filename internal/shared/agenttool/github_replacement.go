package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

type githubPR struct {
	URL                     string
	State                   string
	Body                    string
	HeadRefName             string
	IsCrossRepository       bool
	ClosingIssuesReferences []struct{ URL string }
	Comments                []struct{ Body string }
}

type githubCall func(out io.Writer, args ...string) error
type replacementOwner func(ctx context.Context, runID, prURL, sameRepoHead string) (bool, error)

var replacementLine = regexp.MustCompile(`(?i)^(?:(?:closes|fixes|resolves) #[1-9][0-9]*\.\s+)?` +
	`(?:[-*]\s+)?\*{0,2}(?:replaces|supersedes) #([1-9][0-9]*)\b`)

func reconcileReplacement(ctx context.Context, args []string, out io.Writer, call githubCall,
	owns replacementOwner,
) error {
	if len(args) < 2 || len(args) > 3 || args[1] == "" {
		return fmt.Errorf("usage: gh reconcile-replacement <new GitHub PR URL> <run ID> " +
			"[<replaced PR number from the Task spec>]")
	}
	request, ok := githost.ParseRequest(args[0], githost.KnownKind)
	if !ok || request.Repo.Host != githost.GitHubHost {
		return fmt.Errorf("expected a github.com pull request")
	}
	adapter, _ := githost.For(githost.GitHub)
	newURL := adapter.RequestURL(request.Repo, request.Number)
	if !strings.EqualFold(args[0], newURL) {
		return fmt.Errorf("expected a canonical pull request URL, %s", newURL)
	}
	newPR, err := fetchReplacementPR(call, newURL, "body,state,url,closingIssuesReferences")
	if err != nil {
		return err
	}
	number, err := replacedNumber(args[2:], newPR.Body)
	if err != nil || number == 0 {
		return err
	}
	if number >= request.Number || (newPR.State != "OPEN" && newPR.State != "MERGED") {
		return fmt.Errorf("replacement must be a newer, open or merged pull request")
	}
	oldURL := adapter.RequestURL(request.Repo, number)
	oldPR, err := fetchReplacementPR(call, oldURL,
		"state,url,closingIssuesReferences,headRefName,isCrossRepository,comments")
	if err != nil {
		return err
	}
	if !sameClosingIssue(oldPR, newPR) {
		return fmt.Errorf("refusing to close %s: the two pull requests do not name a shared closing issue", oldURL)
	}
	if oldPR.State == "CLOSED" || oldPR.State == "MERGED" {
		return nil
	}
	if oldPR.State != "OPEN" {
		return fmt.Errorf("unexpected state for %s: %s", oldURL, oldPR.State)
	}
	if owns == nil {
		return fmt.Errorf("refusing to close %s: use the coordinator launcher to verify Run ownership", oldURL)
	}
	head := ""
	if !oldPR.IsCrossRepository {
		head = oldPR.HeadRefName
	}
	owned, err := owns(ctx, args[1], oldURL, head)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("refusing to close %s: it does not belong to Run %s", oldURL, args[1])
	}
	return closeReplacementPR(call, out, oldPR, newPR.URL)
}

func fetchReplacementPR(call githubCall, url, fields string) (githubPR, error) {
	var body bytes.Buffer
	if err := call(&body, "pr", "view", url, "--json", fields); err != nil {
		return githubPR{}, err
	}
	var pr githubPR
	if err := json.Unmarshal(body.Bytes(), &pr); err != nil {
		return githubPR{}, fmt.Errorf("reading GitHub's pull request: %w", err)
	}
	if !strings.EqualFold(pr.URL, url) {
		return githubPR{}, fmt.Errorf("GitHub returned a different pull request than %s", url)
	}
	return pr, nil
}

func replacedNumber(explicit []string, body string) (int, error) {
	number := 0
	if len(explicit) > 0 {
		var err error
		number, err = strconv.Atoi(explicit[0])
		if err != nil || number <= 0 {
			return 0, fmt.Errorf("expected a positive replaced PR number")
		}
	}
	fence := ""
	comment := false
	for line := range strings.SplitSeq(body, "\n") {
		if !comment {
			marker, rest := replacementFence(line)
			if fence != "" {
				if strings.HasPrefix(marker, fence) && strings.TrimSpace(rest) == "" {
					fence = ""
				}
				continue
			}
			if marker != "" {
				fence = marker
				continue
			}
		}
		indented := strings.HasPrefix(line, "    ") || strings.HasPrefix(strings.TrimLeft(line, " "), "\t")
		line, comment = replacementVisibleLine(line, comment)
		if indented {
			continue
		}
		match := replacementLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		claimed, err := strconv.Atoi(match[1])
		if err != nil || (number != 0 && claimed != number) {
			return 0, fmt.Errorf("replacement declarations disagree or are invalid")
		}
		number = claimed
	}
	return number, nil
}

func replacementFence(line string) (string, string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) >= 4 || !(strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
		return "", ""
	}
	rest := strings.TrimLeft(trimmed, trimmed[:1])
	return trimmed[:len(trimmed)-len(rest)], rest
}

func replacementVisibleLine(line string, inComment bool) (string, bool) {
	var visible strings.Builder
	for {
		if inComment {
			_, after, found := strings.Cut(line, "-->")
			if !found {
				return visible.String(), true
			}
			line = after
		}
		before, after, found := strings.Cut(line, "<!--")
		visible.WriteString(before)
		if !found {
			return visible.String(), false
		}
		line, inComment = after, true
	}
}

func sameClosingIssue(oldPR, newPR githubPR) bool {
	for _, oldIssue := range oldPR.ClosingIssuesReferences {
		for _, newIssue := range newPR.ClosingIssuesReferences {
			if oldIssue.URL != "" && strings.EqualFold(oldIssue.URL, newIssue.URL) {
				return true
			}
		}
	}
	return false
}

func closeReplacementPR(call githubCall, out io.Writer, oldPR githubPR, replacement string) error {
	comment := "Replaced by " + replacement + "."
	for _, existing := range oldPR.Comments {
		if strings.TrimSpace(existing.Body) == comment {
			return call(out, "pr", "close", oldPR.URL)
		}
	}
	if err := call(out, "pr", "comment", oldPR.URL, "--body", comment); err != nil {
		return err
	}
	return call(out, "pr", "close", oldPR.URL)
}
