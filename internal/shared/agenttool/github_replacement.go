package agenttool

import (
	"bytes"
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
	ClosingIssuesReferences []struct{ URL string }
	Comments                []struct{ Body string }
}

type githubCall func(out io.Writer, args ...string) error

var replacementLine = regexp.MustCompile(`(?i)^(?:(?:closes|fixes|resolves) #[1-9][0-9]*\.\s+)?` +
	`(?:[-*]\s+)?\*{0,2}(?:replaces|supersedes) #([1-9][0-9]*)\b`)

func reconcileReplacement(args []string, out io.Writer, call githubCall) error {
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf("usage: gh reconcile-replacement <new GitHub PR URL> [<replaced PR number from the Task spec>]")
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
	number, err := replacedNumber(args[1:], newPR.Body)
	if err != nil || number == 0 {
		return err
	}
	if number >= request.Number || (newPR.State != "OPEN" && newPR.State != "MERGED") {
		return fmt.Errorf("replacement must be a newer, open or merged pull request")
	}
	oldURL := adapter.RequestURL(request.Repo, number)
	oldPR, err := fetchReplacementPR(call, oldURL, "state,url,closingIssuesReferences,comments")
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
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
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
