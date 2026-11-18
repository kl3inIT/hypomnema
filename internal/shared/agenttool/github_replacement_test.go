package agenttool

import (
	"bytes"
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
	issues := []struct{ URL string }{{URL: "https://github.com/owner/repo/issues/820"}}
	newPR := githubPR{URL: newURL, Body: "Closes #820. **Supersedes #827**\n", State: "OPEN", ClosingIssuesReferences: issues}
	oldPR := githubPR{URL: oldURL, State: "OPEN", ClosingIssuesReferences: issues}
	var writes [][]string
	commentAllowed, closeAllowed := false, false
	pending := errors.New("waiting for owner approval")
	call := func(out io.Writer, args ...string) error {
		require.Equal(t, "pr", args[0])
		switch args[1] {
		case "view":
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
	require.ErrorIs(t, reconcileReplacement([]string{newURL}, &out, call), pending)
	assert.Empty(t, writes)
	commentAllowed = true
	require.ErrorIs(t, reconcileReplacement([]string{newURL}, &out, call), pending)
	assert.Equal(t, [][]string{{"pr", "comment", oldURL, "--body", comment}}, writes)
	closeAllowed = true
	require.NoError(t, reconcileReplacement([]string{newURL}, &out, call))
	require.NoError(t, reconcileReplacement([]string{newURL}, &out, call))
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
		return json.NewEncoder(out).Encode(githubPR{URL: args[2], State: "OPEN", ClosingIssuesReferences: []struct{ URL string }{{URL: issue}}})
	}
	require.ErrorContains(t, reconcileReplacement([]string{newURL, "827"}, io.Discard, call), "do not name a shared closing issue")
}

func TestReplacementDeclarations(t *testing.T) {
	t.Parallel()

	number, err := replacedNumber(nil, "```\nReplaces #1\n```\nReplaces #827")
	require.NoError(t, err)
	assert.Equal(t, 827, number)
	number, err = replacedNumber([]string{"827"}, "Closes #820")
	require.NoError(t, err)
	assert.Equal(t, 827, number)
	_, err = replacedNumber([]string{"827"}, "Replaces #821")
	require.ErrorContains(t, err, "disagree")
}
