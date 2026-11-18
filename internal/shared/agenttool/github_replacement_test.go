package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplacementCommentsAndClosesWithSeparateApprovals(t *testing.T) {
	t.Parallel()

	const newURL = "https://github.com/owner/repo/pull/840"
	const oldURL = "https://github.com/owner/repo/pull/827"
	const comment = "Replaced by " + newURL + "."
	const oldSHA, newSHA = "old-sha", "new-sha"
	issues := []struct{ URL string }{{URL: "https://github.com/owner/repo/issues/820"}}
	newPR := githubPR{
		URL: newURL, Body: "Closes #820. **Supersedes #827**\n", State: "OPEN", ClosingIssuesReferences: issues,
		HeadRefName: "hyp/tsk_new", HeadRefOid: newSHA, HeadRepository: struct{ NameWithOwner string }{"owner/repo"},
	}
	oldPR := githubPR{URL: oldURL, State: "OPEN", ClosingIssuesReferences: issues,
		HeadRefName: "hyp/tsk_old", HeadRefOid: oldSHA, HeadRepository: newPR.HeadRepository}
	var writes [][]string
	commentAllowed, closeAllowed := false, false
	pending := errors.New("waiting for owner approval")
	call := func(out io.Writer, args ...string) error {
		require.Equal(t, "pr", args[0])
		switch args[1] {
		case "view":
			assert.Contains(t, args[4], "headRefOid")
			if args[2] == newURL {
				return json.NewEncoder(out).Encode(newPR)
			}
			require.Equal(t, oldURL, args[2])
			return json.NewEncoder(out).Encode(oldPR)
		case "comment":
			if !commentAllowed {
				return pending
			}
			oldPR.Comments = append(oldPR.Comments, struct{ Body string }{Body: args[4]})
		case "close":
			if !closeAllowed {
				return pending
			}
			oldPR.State = "CLOSED"
		default:
			t.Fatalf("unexpected GitHub command %q", args)
		}
		writes = append(writes, args)
		return nil
	}

	var out bytes.Buffer
	reconcile := func() error {
		return reconcileReplacement(t.Context(), []string{newURL, "run_1"}, &out, call,
			func(_ context.Context, runID, prURL, head, sha string) (bool, error) {
				assert.Equal(t, "run_1", runID)
				return (prURL == oldURL && head == oldPR.HeadRefName && sha == oldSHA) ||
					(prURL == newURL && head == newPR.HeadRefName && sha == newSHA), nil
			})
	}
	require.ErrorIs(t, reconcile(), pending)
	assert.Empty(t, writes)
	commentAllowed = true
	require.ErrorIs(t, reconcile(), pending)
	assert.Equal(t, [][]string{{"pr", "comment", oldURL, "--body", comment}}, writes)
	closeAllowed = true
	require.NoError(t, reconcile())
	require.NoError(t, reconcile())
	assert.Equal(t, [][]string{{"pr", "comment", oldURL, "--body", comment}, {"pr", "close", oldURL}}, writes)
	assert.Equal(t, "CLOSED", oldPR.State)
	assert.Equal(t, "OPEN", newPR.State)
}

func TestReplacementRequiresSharedIssue(t *testing.T) {
	t.Parallel()

	const newURL = "https://github.com/owner/repo/pull/840"
	const oldURL = "https://github.com/owner/repo/pull/827"
	call := func(out io.Writer, args ...string) error {
		require.Equal(t, "view", args[1], "cannot write without a shared issue")
		issue := "https://github.com/owner/repo/issues/820"
		if args[2] == oldURL {
			issue = "https://github.com/owner/repo/issues/822"
		}
		return json.NewEncoder(out).Encode(githubPR{
			URL: args[2], State: "OPEN", ClosingIssuesReferences: []struct{ URL string }{{URL: issue}},
			HeadRefName: "hyp/tsk_mine", HeadRefOid: "sha", HeadRepository: struct{ NameWithOwner string }{"owner/repo"},
		})
	}
	err := reconcileReplacement(t.Context(), []string{newURL, "run_1", "827"}, io.Discard, call,
		func(context.Context, string, string, string, string) (bool, error) { return true, nil })
	require.ErrorContains(t, err, "do not name a shared closing issue")
}

func TestReplacementRefusesUnownedPRWithSameIssue(t *testing.T) {
	t.Parallel()

	const newURL = "https://github.com/owner/repo/pull/840"
	call := func(out io.Writer, args ...string) error {
		require.Equal(t, "view", args[1], "a shared issue is not ownership proof")
		return json.NewEncoder(out).Encode(githubPR{
			URL: args[2], State: "OPEN", HeadRefName: "hyp/tsk_other", IsCrossRepository: true,
			ClosingIssuesReferences: []struct{ URL string }{{URL: "https://github.com/owner/repo/issues/820"}},
		})
	}
	err := reconcileReplacement(t.Context(), []string{newURL, "run_1", "827"}, io.Discard, call,
		func(context.Context, string, string, string, string) (bool, error) {
			t.Fatal("a fork's branch is not proof")
			return false, nil
		})
	require.ErrorContains(t, err, "does not belong")
}

func TestReplacementDeclarations(t *testing.T) {
	t.Parallel()

	number, err := replacedNumber(nil, "<!--\nReplaces #2\n-->\n"+
		"````text\n```\nReplaces #1\n````\n    Replaces #3\nAn example is `` `<!--` ``.\nReplaces #827")
	require.NoError(t, err)
	assert.Equal(t, 827, number)
	number, err = replacedNumber([]string{"827"}, "Closes #820")
	require.NoError(t, err)
	assert.Equal(t, 827, number)
	_, err = replacedNumber([]string{"827"}, "Replaces #821")
	require.ErrorContains(t, err, "disagree")
}

func TestReplacementAfterMultilineInlineCode(t *testing.T) {
	t.Parallel()

	number, err := replacedNumber(nil, "An example: `<!--\nReplaces #123`\n\nReplaces #827")
	require.NoError(t, err)
	assert.Equal(t, 827, number)
	number, err = replacedNumber(nil, "`Example:` Replaces #123")
	require.NoError(t, err)
	assert.Zero(t, number)
}

func TestReplacementRecognizesGitHubClosingKeywords(t *testing.T) {
	t.Parallel()

	const repo = "https://github.com/owner/repo"
	keywords := []string{"close", "closes", "closed", "fix", "fixes", "fixed", "resolve", "resolves", "resolved"}
	for _, keyword := range keywords {
		assert.Equal(t, []string{repo + "/issues/820", "https://github.com/other/repo/issues/730"},
			replacementIssues(repo, githubPR{Body: keyword + ": #820, " + keyword + " other/repo#730"}))
	}
}

func TestReplacementRefusesUnownedNewPR(t *testing.T) {
	t.Parallel()

	const newURL = "https://github.com/owner/repo/pull/840"
	const oldURL = "https://github.com/owner/repo/pull/827"
	call := func(out io.Writer, args ...string) error {
		require.Equal(t, "view", args[1], "no write when the replacement is from a fork")
		isFork := args[2] == newURL
		return json.NewEncoder(out).Encode(githubPR{
			URL: args[2], State: "OPEN", Body: "Replaces #827", HeadRefName: "hyp/tsk_mine", HeadRefOid: "sha",
			IsCrossRepository:       isFork,
			HeadRepository:          struct{ NameWithOwner string }{"owner/repo"},
			ClosingIssuesReferences: []struct{ URL string }{{URL: "https://github.com/owner/repo/issues/820"}},
		})
	}
	err := reconcileReplacement(t.Context(), []string{newURL, "run_1"}, io.Discard, call,
		func(_ context.Context, _, prURL, head, _ string) (bool, error) {
			return prURL == oldURL && head == "hyp/tsk_mine", nil
		})
	require.ErrorContains(t, err, "does not belong")
}

func TestReplacementUsesVisibleClosingKeywordWithoutGitHubClosingReferences(t *testing.T) {
	t.Parallel()

	const newURL = "https://github.com/owner/repo/pull/840"
	const oldURL = "https://github.com/owner/repo/pull/827"
	bodies := []string{"Fixes #820", "CLOSES: #820", "<!-- Fixes #820 -->\nFixes #822", "Example: `Fixes #820`"}
	for _, oldBody := range bodies {
		t.Run(oldBody, func(t *testing.T) {
			t.Parallel()

			var writes []string
			call := func(out io.Writer, args ...string) error {
				if args[1] != "view" {
					assert.Equal(t, oldURL, args[2])
					writes = append(writes, args[1])
					return nil
				}
				body, head := oldBody, "hyp/tsk_old"
				if args[2] == newURL {
					body, head = "Closes #820\nReplaces #827", "hyp/tsk_new"
				}
				return json.NewEncoder(out).Encode(githubPR{
					URL: args[2], State: "OPEN", Body: body, HeadRefName: head, HeadRefOid: "sha",
					HeadRepository: struct{ NameWithOwner string }{"owner/repo"},
				})
			}
			err := reconcileReplacement(t.Context(), []string{newURL, "run_1"}, io.Discard, call,
				func(_ context.Context, _, _, head, _ string) (bool, error) {
					return head == "hyp/tsk_old" || head == "hyp/tsk_new", nil
				})
			if oldBody == "Fixes #820" || oldBody == "CLOSES: #820" {
				require.NoError(t, err)
				assert.Equal(t, []string{"comment", "close"}, writes)
				return
			}
			require.ErrorContains(t, err, "do not name a shared closing issue")
			assert.Empty(t, writes)
		})
	}
}
