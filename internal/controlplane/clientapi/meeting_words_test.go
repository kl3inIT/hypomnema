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
// is fixed over the API in one line, and the right word joins the owner's list. The owner makes, changes and
// deletes a notes format of their own, offered after the built-in ones; changing a built-in one is refused, and a
// deleted one is no format to write notes in.
func TestTheOwnerKeepsWordsAndFormatsOverTheAPI(t *testing.T) {
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
	named, err := owner.NameMeetingSpeakerWithResponse(ctx, id, "2", openapi.SpeakerName{Name: "Laura"})
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

	made, err := owner.CreateMeetingFormatWithResponse(ctx, openapi.MeetingFormatInput{
		Name: "Weekly ops", Sections: []openapi.MeetingFormatSectionInput{
			{Title: "Incidents", About: ptr("What broke and how it was fixed")}, {Title: "On call"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))
	assert.False(t, made.JSON201.Builtin)
	assert.Equal(t, []openapi.MeetingFormatSection{
		{Title: "Incidents", About: "What broke and how it was fixed"}, {Title: "On call"},
	}, made.JSON201.Sections)

	changed, err := owner.UpdateMeetingFormatWithResponse(ctx, made.JSON201.Id, openapi.MeetingFormatInput{
		Name: "Weekly ops review", Sections: []openapi.MeetingFormatSectionInput{{Title: "Incidents"}},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, changed.StatusCode(), string(changed.Body))
	assert.Equal(t, "Weekly ops review", changed.JSON200.Name)

	settings, err := pi.GetMeetingSettingsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, settings.StatusCode(), string(settings.Body))

	formats := settings.JSON200.Formats
	assert.Equal(t, "Weekly ops review", formats[len(formats)-1].Name, "the owner's follow the built-in ones")
	assert.True(t, formats[0].Builtin)

	builtin, err := owner.UpdateMeetingFormatWithResponse(ctx, "general", openapi.MeetingFormatInput{
		Name: "Mine", Sections: []openapi.MeetingFormatSectionInput{{Title: "X"}},
	})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, builtin.Body, builtin.StatusCode(), http.StatusBadRequest))

	gone, err := owner.DeleteMeetingFormatWithResponse(ctx, made.JSON201.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, gone.StatusCode())

	notes, err := owner.WriteMeetingNotesWithResponse(ctx, id,
		openapi.WriteMeetingNotesRequest{Format: made.JSON201.Id})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, notes.Body, notes.StatusCode(), http.StatusBadRequest),
		"a deleted format is no format")
}
