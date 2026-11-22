package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
)

// Scenario: the owner fixes words in a meeting within one millisecond; the right words join the list in the order
// they were fixed, not in the alphabet's (issue #758).
func TestWordsFixedInOneMillisecondKeepTheOrderTheyWereFixed(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()
	m := s.Meetings()

	meeting, err := m.Create(ctx, meetingstore.NewMeeting{
		ID: m.NewID(), FileName: "sync.mp3", Size: 1,
	})
	require.NoError(t, err)
	require.NoError(t, m.SaveTranscript(ctx, meeting.ID, meetingstore.Heard{
		Lines: []meetingstore.HeardLine{{Text: "Ok, mình là Lan."}},
	}))

	for _, fix := range []meetingstore.Fix{{From: "Ok", To: "Okay"}, {From: "Lan", To: "Laura"}} {
		_, err := m.ReplaceWord(ctx, meeting.ID, fix)
		require.NoError(t, err)
	}

	words, err := m.Words(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"Okay", "Laura"}, words)
}

// Scenario: review of #1037, round 3: a meeting the owner titled is transcribed at 09:00 and its notes are written
// at 10:00, which moves only its notes. Listed, it says when its notes were written, so that the Thư ký's
// meeting_list finds it among the meetings since 09:01.
func TestAListedMeetingSaysWhenItsNotesWereWritten(t *testing.T) {
	t.Parallel()

	s, _, clock := newTestService(t)
	ctx := t.Context()
	m := s.Meetings()

	meeting, err := m.Create(ctx, meetingstore.NewMeeting{ID: m.NewID(), Title: "Kai sync", FileName: "kai.mp3", Size: 1})
	require.NoError(t, err)
	require.NoError(t, m.SaveTranscript(ctx, meeting.ID, meetingstore.Heard{
		Lines: []meetingstore.HeardLine{{Text: "Để em gửi báo giá."}},
	}))

	clock.now = clock.now.Add(time.Hour)
	_, claimed, err := m.ClaimNotes(ctx, meeting.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, m.SaveNotes(ctx, meeting.ID, meetingstore.Written{Model: "m", Summary: "A quote is sent."}))

	listed, err := m.List(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, meetingstore.NotesReady, listed[0].Notes.State)
	require.NotNil(t, listed[0].Notes.WrittenAt, "the list says when the notes were written")
	assert.Equal(t, clock.now, *listed[0].Notes.WrittenAt)
}
