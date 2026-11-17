package orchestration

import (
	"testing"

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
		ID: m.NewID(), FileName: "sync.mp3", Format: "summary", Size: 1,
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
