package clientapi

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// twoVoices stands for the speech service: two voices, the second saying who she is.
type twoVoices struct{}

func (twoVoices) Transcribe(context.Context, speech.TranscribeRequest) (speech.Transcript, error) {
	return speech.Transcript{
		Duration: 4 * time.Second, Provider: "soniox", Model: "stt-async-v5",
		Segments: []speech.Segment{
			{Start: 0, End: time.Second, Text: "Let's ship on Friday.", Speaker: "1"},
			{Start: time.Second, End: 4 * time.Second, Text: "I'm Laura. I will draft the email.", Speaker: "2"},
		},
	}, nil
}

// noModel stands for a gateway with no chat model asked: the notes are not this test's.
type noModel struct{}

func (noModel) Complete(context.Context, string, []byte) ([]byte, error) {
	return nil, &modelgateway.CompletionError{Message: "no model"}
}

type noUsage struct{}

func (noUsage) MeetingsKey(context.Context) (string, error)                   { return "gwk_x", nil }
func (noUsage) RecordAudioCall(context.Context, modelgateway.AudioCall) error { return nil }

// Scenario: the owner uploads a recording over the API with its people and words; a coordinator's token may read
// meetings but not upload one. Once heard, the meeting answers its transcript with the voices' shares and the name
// a voice gave itself; the recording plays back, a Range answered 206 with that part so that the player seeks; the
// owner names a voice; deleting the meeting makes it gone.
func TestTheOwnerUploadsAMeetingAndPlaysItBack(t *testing.T) {
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

	params := &openapi.UploadMeetingParams{
		FileName: "sync.mp3", Language: ptr("en"), People: &[]string{"Laura", "Kai"}, Terms: &[]string{"MapOps"},
	}

	refused, err := pi.UploadMeetingWithBodyWithResponse(ctx, params, "application/octet-stream",
		strings.NewReader("ID3-0123456789"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "only the owner uploads")

	up, err := owner.UploadMeetingWithBodyWithResponse(ctx, params, "application/octet-stream",
		strings.NewReader("ID3-0123456789"))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, up.StatusCode(), string(up.Body))

	m := up.JSON201
	assert.Equal(t, openapi.MeetingStateTranscribing, m.State)
	assert.Equal(t, "audio/mpeg", m.MediaType, "told from the file's bytes")
	assert.Equal(t, []string{"Laura", "Kai"}, m.People)
	assert.Equal(t, []string{"MapOps"}, m.Terms)

	work.Work(ctx)

	got, err := pi.GetMeetingWithResponse(ctx, m.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.StatusCode(), string(got.Body))

	d := got.JSON200
	assert.Equal(t, openapi.MeetingStateTranscribed, d.Meeting.State)
	require.Len(t, d.Lines, 2)
	require.Len(t, d.Meeting.Speakers, 2)
	assert.InDelta(t, 0.25, d.Meeting.Speakers[0].Share, 0.001)
	assert.InDelta(t, 0.75, d.Meeting.Speakers[1].Share, 0.001)
	assert.Equal(t, []openapi.SpeakerSuggestion{{Label: "2", Name: "Laura", Line: 2}}, d.Suggestions)
	assert.Equal(t, openapi.MeetingNotesStateFailed, d.Meeting.Notes.State, "no notes model is picked")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url+BasePath+"/meetings/"+m.Id+"/recording", nil)
	require.NoError(t, err)
	req.Header.Set("Range", "bytes=4-7")

	_, ownerSecret, err := w.svc.AddToken(ctx, "player", bearertoken.Owner)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+ownerSecret)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	part, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, "0123", string(part))
	assert.Equal(t, "audio/mpeg", resp.Header.Get("Content-Type"))
	assert.Equal(t, "bytes 4-7/14", resp.Header.Get("Content-Range"))

	named, err := owner.NameMeetingSpeakerWithResponse(ctx, m.Id, "2", openapi.SpeakerName{Name: "Laura"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, named.StatusCode(), string(named.Body))
	assert.Equal(t, "Laura", named.JSON200.Meeting.Speakers[1].Name)
	assert.Empty(t, named.JSON200.Suggestions, "a named voice is offered nothing")

	gone, err := owner.DeleteMeetingWithResponse(ctx, m.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, gone.StatusCode())

	after, err := owner.GetMeetingWithResponse(ctx, m.Id)
	require.NoError(t, err)
	assert.Equal(t, openapi.NotFound, errorCode(t, after.Body, after.StatusCode(), http.StatusNotFound))
}
