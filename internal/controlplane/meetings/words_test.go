package meetings_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
)

// Scenario (issue #747): the owner keeps a word list once; a later meeting is heard with it and with the names
// found elsewhere: the name the owner gave a voice in an earlier meeting, the Bot's and the registered repository's.
// Each word is given once, the owner's first, and the list says where each comes from.
func TestEveryTranscriptionIsGivenTheWordsAndTheNamesFound(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.orch.AddRepo(ctx, "shop", "acme/shop", "", "", "", "")
	require.NoError(t, err)
	require.NoError(t, w.svc.SetWords(ctx, []string{"MapOps", " ", "mapops", "Lan"}))

	first := w.upload(t, meetings.Upload{})
	w.svc.Work(ctx)
	require.NoError(t, w.svc.NameSpeaker(ctx, first.ID, "2", "Lan"))
	require.NoError(t, w.svc.NameSpeaker(ctx, first.ID, "1", "Minh"))

	words, err := w.svc.Words(ctx)
	require.NoError(t, err)
	assert.Equal(t, []meetings.Word{
		{Text: "MapOps", From: meetings.FromOwner, Sent: true},
		{Text: "Lan", From: meetings.FromOwner, Sent: true},
		{Text: "Minh", From: meetings.FromSpeaker, Sent: true},
		{Text: "coordinator", From: meetings.FromBot, Sent: true},
		{Text: "shop", From: meetings.FromRepository, Sent: true},
	}, words, "a word given twice, or by the owner and a voice, is listed once")

	w.upload(t, meetings.Upload{})
	w.svc.Work(ctx)

	assert.Equal(t, map[string]any{"terms": []any{"MapOps", "Lan", "Minh", "coordinator", "shop"}},
		w.soniox.job["context"], "the next meeting is heard with every word")
}

// Scenario: the owner's list is full; the names found past the bound are listed but not sent, so that Soniox's
// context stays inside what it takes. One word more than the bound is refused.
func TestTheWordsSentAreBounded(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	own := make([]string, 0, meetingstore.MaxWords)
	for i := range meetingstore.MaxWords {
		own = append(own, fmt.Sprintf("word-%03d", i))
	}

	require.NoError(t, w.svc.SetWords(ctx, own))
	require.ErrorIs(t, w.svc.SetWords(ctx, append(own, "one-more")), orchestration.ErrInvalid)

	words, err := w.svc.Words(ctx)
	require.NoError(t, err)
	require.Len(t, words, meetings.MaxSentWords+1)
	assert.Equal(t, meetings.Word{Text: "coordinator", From: meetings.FromBot, Sent: false}, words[len(words)-1])

	w.upload(t, meetings.Upload{})
	w.svc.Work(ctx)

	terms, _ := w.soniox.job["context"].(map[string]any)["terms"].([]any)
	assert.Len(t, terms, meetings.MaxSentWords)
	assert.NotContains(t, terms, "coordinator")
}

// Scenario: a word heard wrong is fixed in one line, then in the whole meeting, in any case and only as a whole word;
// the right word joins the owner's list once. The notes are not written again by the fix. A word the meeting does
// not say, a line it does not have, or no word at all is refused.
func TestAWordHeardWrongIsFixedInALineOrTheWholeMeeting(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	m := w.upload(t, meetings.Upload{})
	w.svc.Work(ctx)
	require.Len(t, w.model.asked, 1)

	// Lines: 1 "Hôm nay mình chốt ra mắt thứ Sáu.", 2 "Chào mọi người, mình là Lan. Em sẽ gửi …", 3 "Ok vậy nhé."
	fixed, err := w.svc.ReplaceWord(ctx, m.ID, meetingstore.Fix{From: "mình", To: "chúng ta", Line: 1})
	require.NoError(t, err)
	assert.Equal(t, meetingstore.Fixed{Replaced: 1, Learned: true}, fixed)

	d, err := w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Hôm nay chúng ta chốt ra mắt thứ Sáu.", d.Lines[0].Text)
	assert.Contains(t, d.Lines[1].Text, "mình là Lan", "another line is left as it was")

	fixed, err = w.svc.ReplaceWord(ctx, m.ID, meetingstore.Fix{From: "OK", To: "Okay"})
	require.NoError(t, err)
	assert.Equal(t, 1, fixed.Replaced, "any case")

	fixed, err = w.svc.ReplaceWord(ctx, m.ID, meetingstore.Fix{From: "Lan", To: "Laura"})
	require.NoError(t, err)
	assert.Equal(t, 1, fixed.Replaced)

	d, err = w.svc.Get(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Okay vậy nhé.", d.Lines[2].Text)
	assert.Contains(t, d.Lines[1].Text, "mình là Laura.")

	_, err = w.svc.ReplaceWord(ctx, m.ID, meetingstore.Fix{From: "chúng", To: "các bạn"})
	require.NoError(t, err)

	// "ta" is inside no other word, but "Sá" is inside "Sáu": a part of a word is not a word.
	_, err = w.svc.ReplaceWord(ctx, m.ID, meetingstore.Fix{From: "Sá", To: "Sa"})
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	_, err = w.svc.ReplaceWord(ctx, m.ID, meetingstore.Fix{From: "Laura", To: "Lan", Line: 9})
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	_, err = w.svc.ReplaceWord(ctx, m.ID, meetingstore.Fix{From: "", To: "Lan"})
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	words, err := w.store.Words(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"chúng ta", "Okay", "Laura", "các bạn"}, words, "each right word joins the list once")

	w.svc.Work(ctx)
	assert.Len(t, w.model.asked, 1, "the notes are written again only when the owner asks")
}

// Scenario: the owner makes a notes format; a meeting uploaded in it is written up with its sections, and written
// again in it after a change. A built-in format is not changed or deleted. Once the owner deletes it, it is no
// format to upload or write in; notes still waiting in it are written in the default one.
func TestTheOwnersFormatsAreMadeChangedAndUsed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	f, err := w.svc.CreateFormat(ctx, " Weekly ops ", []meetings.FormatSection{
		{Title: "Incidents", About: "What broke,\nand how it was fixed"}, {Title: " "}, {Title: "On call"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Weekly ops", f.Name)
	assert.Equal(t, []meetings.FormatSection{
		{Title: "Incidents", About: "What broke,\nand how it was fixed"}, {Title: "On call"},
	}, f.Sections, "an empty section is dropped")

	for _, bad := range [][]meetings.FormatSection{nil, {{About: "no title"}}, make([]meetings.FormatSection, 21)} {
		for i := range bad {
			if bad[i].About == "" {
				bad[i] = meetings.FormatSection{Title: fmt.Sprint("S", i)}
			}
		}

		_, err := w.svc.CreateFormat(ctx, "Bad", bad)
		require.ErrorIs(t, err, orchestration.ErrInvalid)
	}

	m := w.upload(t, meetings.Upload{Format: f.ID})
	w.svc.Work(ctx)

	require.Len(t, w.model.asked, 1)
	assert.Contains(t, w.model.asked[0], "Incidents: What broke, and how it was fixed")
	assert.Contains(t, w.model.asked[0], "2. On call\\n")

	_, err = w.svc.UpdateFormat(ctx, f.ID, "Weekly ops", []meetings.FormatSection{{Title: "Pages sent"}})
	require.NoError(t, err)
	require.NoError(t, w.svc.WriteAgain(ctx, m.ID, f.ID))
	w.svc.Work(ctx)
	assert.Contains(t, w.model.asked[1], "Pages sent")

	_, err = w.svc.UpdateFormat(ctx, "general", "Mine", []meetings.FormatSection{{Title: "X"}})
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	require.ErrorIs(t, w.svc.DeleteFormat(ctx, "standup"), orchestration.ErrInvalid)

	waiting := w.upload(t, meetings.Upload{Format: f.ID})
	require.NoError(t, w.svc.DeleteFormat(ctx, f.ID))
	require.ErrorIs(t, w.svc.DeleteFormat(ctx, f.ID), orchestration.ErrNotFound)

	_, err = w.svc.Upload(ctx, meetings.Upload{Body: nil, FileName: "x.mp3", Format: f.ID})
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	w.svc.Work(ctx)

	require.Len(t, w.model.asked, 3)
	assert.Contains(t, w.model.asked[2], "Discussion highlights", "a deleted format's notes are in the default one")

	d, err := w.svc.Get(ctx, waiting.ID)
	require.NoError(t, err)
	assert.Equal(t, meetingstore.NotesReady, d.Meeting.Notes.State)

	all, err := w.svc.Formats(ctx)
	require.NoError(t, err)
	assert.Len(t, all, len(meetings.Builtins()), "only the built-in ones are left")
}
