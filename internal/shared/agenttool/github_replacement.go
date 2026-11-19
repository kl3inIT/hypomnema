package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

type githubPR struct {
	URL                     string
	State                   string
	Body                    string
	HeadRefName             string
	HeadRefOid              string
	HeadRepository          struct{ NameWithOwner string }
	IsCrossRepository       bool
	ClosingIssuesReferences []struct{ URL string }
	Comments                []struct{ Body string }
}

type githubCall func(out io.Writer, args ...string) error
type replacementOwner func(ctx context.Context, runID, prURL, branch, sha string) (bool, error)

const closingKeyword = `(?:close[sd]?|fix(?:es|ed)?|resolve[sd]?):?[ \t]+`
const closingRepo = `[a-z0-9_.-]+/[a-z0-9_.-]+`

var replacementLine = regexp.MustCompile(`(?i)^(?:` + closingKeyword + `(?:` + closingRepo + `)?#[1-9][0-9]*\.\s+)?` +
	`(?:[-*]\s+)?\*{0,2}(?:replaces|supersedes) #([1-9][0-9]*)\b`)

var closingIssue = regexp.MustCompile(`(?i)\b` + closingKeyword + `(` + closingRepo + `)?#([1-9][0-9]*)\b`)

const replacementPRFields = "body,state,url,headRefName,headRefOid,headRepository," +
	"isCrossRepository,closingIssuesReferences"

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
	newPR, err := fetchReplacementPR(call, newURL, replacementPRFields)
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
	oldPR, err := fetchReplacementPR(call, oldURL, replacementPRFields+",comments")
	if err != nil {
		return err
	}
	if oldPR.State == "CLOSED" || oldPR.State == "MERGED" {
		return nil
	}
	if oldPR.State != "OPEN" {
		return fmt.Errorf("unexpected state for %s: %s", oldURL, oldPR.State)
	}
	for _, pr := range []githubPR{oldPR, newPR} {
		if err := verifyReplacementOwner(ctx, owns, args[1], request.Repo.Path, pr); err != nil {
			return err
		}
	}
	repoURL := "https://" + request.Repo.Host + "/" + request.Repo.Path
	if !sameClosingIssue(repoURL, oldPR, newPR) {
		return fmt.Errorf("refusing to close %s: the two pull requests do not name a shared closing issue", oldURL)
	}
	return closeReplacementPR(call, out, oldPR, newPR.URL)
}

func verifyReplacementOwner(ctx context.Context, owns replacementOwner, runID, repo string, pr githubPR) error {
	if owns == nil {
		return fmt.Errorf("refusing to reconcile %s: use the coordinator launcher to verify Run ownership", pr.URL)
	}
	if pr.IsCrossRepository || !strings.EqualFold(pr.HeadRepository.NameWithOwner, repo) || pr.HeadRefName == "" ||
		pr.HeadRefOid == "" {
		return fmt.Errorf("refusing to reconcile %s: its head does not belong to the repository of Run %s", pr.URL, runID)
	}
	owned, err := owns(ctx, runID, pr.URL, pr.HeadRefName, pr.HeadRefOid)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("refusing to reconcile %s: it does not belong to Run %s", pr.URL, runID)
	}
	return nil
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
	for line := range replacementBodyLines(body) {
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

func replacementBodyLines(body string) iter.Seq[string] {
	return func(yield func(string) bool) {
		source := []byte(body)
		doc := goldmark.DefaultParser().Parse(text.NewReader(source))
		_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch node.Kind() {
			case ast.KindBlockquote, ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindHTMLBlock:
				return ast.WalkSkipChildren, nil
			case ast.KindParagraph, ast.KindTextBlock:
				for line := range strings.SplitSeq(replacementText(node, source), "\n") {
					if !yield(strings.TrimSpace(line)) {
						return ast.WalkStop, nil
					}
				}
				return ast.WalkSkipChildren, nil
			default:
				return ast.WalkContinue, nil
			}
		})
	}
}

func replacementText(block ast.Node, source []byte) string {
	var visible strings.Builder
	_ = ast.Walk(block, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.CodeSpan, *ast.RawHTML, *ast.Image:
			visible.WriteByte(0)
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			visible.Write(node.Segment.Value(source))
			if node.SoftLineBreak() || node.HardLineBreak() {
				visible.WriteByte('\n')
			}
		case *ast.String:
			visible.Write(node.Value)
		}
		return ast.WalkContinue, nil
	})
	return visible.String()
}

func sameClosingIssue(repoURL string, oldPR, newPR githubPR) bool {
	for _, oldIssue := range replacementIssues(repoURL, oldPR) {
		for _, newIssue := range replacementIssues(repoURL, newPR) {
			if oldIssue != "" && strings.EqualFold(oldIssue, newIssue) {
				return true
			}
		}
	}
	return false
}

func replacementIssues(repoURL string, pr githubPR) []string {
	var issues []string
	for _, issue := range pr.ClosingIssuesReferences {
		if issue.URL != "" {
			issues = append(issues, issue.URL)
		}
	}
	if len(issues) == 0 {
		for line := range replacementBodyLines(pr.Body) {
			for _, match := range closingIssue.FindAllStringSubmatch(line, -1) {
				issueRepo := repoURL
				if match[1] != "" {
					issueRepo = "https://github.com/" + match[1]
				}
				issues = append(issues, issueRepo+"/issues/"+match[2])
			}
		}
	}
	return issues
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
