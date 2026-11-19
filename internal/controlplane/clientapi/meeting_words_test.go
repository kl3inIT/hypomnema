package clientapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
)

// Scenario (issue #747): the owner keeps the words to hear right once and every token reads them, with the name the
// owner gave a speaker and the Bot's name among them; a coordinator's token may not change them. A word heard wrong
// is fixed over the API in one line, and the right word joins the owner's list.
func TestTheOwnerKeepsWordsOverTheAPI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	work, err := meetings.New(meetings.Config{
		Dir: filepath.Join(t.TempDir(), "meetings"), Store: w.svc.Meetings(), Speech: twoVoices{}, Models: noModel{},
		Usage: noUsage{},
	})
	require.NoError(t, err)
	w.srv.WithMeetings(work)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	refused, err := pi.SetMeetingWordsWithResponse(ctx, openapi.MeetingWordsUpdate{Words: []string{"MapOps"}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "only the owner sets the words")

	set, err := owner.SetMeetingWordsWithResponse(ctx, openapi.MeetingWordsUpdate{Words: []string{"MapOps", "ĐKSD"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode(), string(set.Body))
	assert.Equal(t, meetings.MaxSentWords, set.JSON200.MaxSent)

	up, err := owner.UploadMeetingWithBodyWithResponse(ctx, &openapi.UploadMeetingParams{FileName: "sync.mp3"},
		"application/octet-stream", strings.NewReader("ID3-0123456789"))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, up.StatusCode(), string(up.Body))

	work.Work(ctx)

	id := up.JSON201.Id
	named, err := owner.NameMeetingSpeakerWithResponse(ctx, id, "2", nil, openapi.SpeakerName{Name: "Laura"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, named.StatusCode(), string(named.Body))

	fixed, err := owner.FixMeetingWordWithResponse(ctx, id, openapi.MeetingWordFix{
		From: "friday", To: "Friday release", Line: ptr(1),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, fixed.StatusCode(), string(fixed.Body))
	assert.Equal(t, 1, fixed.JSON200.Replaced)
	assert.True(t, fixed.JSON200.Learned)
	assert.Equal(t, "Let's ship on Friday release.", fixed.JSON200.Detail.Lines[0].Text)

	missing, err := owner.FixMeetingWordWithResponse(ctx, id, openapi.MeetingWordFix{From: "Tuesday", To: "Monday"})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest,
		errorCode(t, missing.Body, missing.StatusCode(), http.StatusBadRequest), "the meeting does not say it")

	words, err := pi.ListMeetingWordsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, words.StatusCode(), string(words.Body))
	assert.Equal(t, []openapi.MeetingWord{
		{Word: "MapOps", From: openapi.MeetingWordSourceOwner, Sent: true},
		{Word: "ĐKSD", From: openapi.MeetingWordSourceOwner, Sent: true},
		{Word: "Friday release", From: openapi.MeetingWordSourceOwner, Sent: true},
		{Word: "Laura", From: openapi.MeetingWordSourceSpeaker, Sent: true},
		{Word: "coordinator", From: openapi.MeetingWordSourceBot, Sent: true},
	}, words.JSON200.Data)
}
