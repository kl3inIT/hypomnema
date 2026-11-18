package clientapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

func (twoVoices) Live(context.Context, speech.LiveRequest, speech.LiveListener) (*speech.Live, error) {
	return nil, speech.ErrUnavailable
}

// noModel stands for a gateway with no chat model asked: the notes are not this test's.
type noModel struct{}

func (noModel) Complete(context.Context, string, []byte) ([]byte, error) {
	return nil, &modelgateway.CompletionError{Message: "no model"}
}

type noUsage struct{}

func (noUsage) MeetingsKey(context.Context) (string, error)                   { return "gwk_x", nil }
func (noUsage) RecordAudioCall(context.Context, modelgateway.AudioCall) error { return nil }

// Scenario: the owner uploads a recording over the API with its people; a coordinator's token may read
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
		FileName: "sync.mp3", Language: ptr("en"), People: &[]string{"Laura", "Kai"},
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
	assert.Equal(t, []openapi.SpeakerSuggestion{
		{Track: openapi.MeetingTrackFile, Label: "2", Name: "Laura", Line: 2},
	}, d.Suggestions)
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

	named, err := owner.NameMeetingSpeakerWithResponse(ctx, m.Id, "2", nil, openapi.SpeakerName{Name: "Laura"})
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

// Scenario: a page uploaded as a recording, declared text/html. It is kept as application/octet-stream, and played
// back with nosniff, a sandboxing policy and an inline disposition with its name, so that the control plane's origin
// never renders it as a page; a real recording keeps the sound type its bytes show.
func TestARecordingIsNeverServedAsAPage(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	work, err := meetings.New(meetings.Config{
		Dir: filepath.Join(t.TempDir(), "meetings"), Store: w.svc.Meetings(), Speech: twoVoices{}, Models: noModel{},
		Usage: noUsage{},
	})
	require.NoError(t, err)
	w.srv.WithMeetings(work)

	_, secret, err := w.svc.AddToken(ctx, "web", bearertoken.Owner)
	require.NoError(t, err)

	owner := w.client(secret)
	page := "<html><script>alert(document.cookie)</script></html>"

	up, err := owner.UploadMeetingWithBodyWithResponse(ctx, &openapi.UploadMeetingParams{
		FileName: "x\".html", MediaType: ptr("text/html"),
	}, "application/octet-stream", strings.NewReader(page))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, up.StatusCode(), string(up.Body))
	assert.Equal(t, "application/octet-stream", up.JSON201.MediaType)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		w.url+BasePath+"/meetings/"+up.JSON201.Id+"/recording", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, page, string(body))
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox; default-src 'none'", resp.Header.Get("Content-Security-Policy"))
	assert.Equal(t, `inline; filename="x\".html"`, resp.Header.Get("Content-Disposition"))

	for declared, want := range map[string]string{
		"audio/mp4":                "audio/mp4",
		"text/html; charset=utf-8": "audio/mpeg",
		"":                         "audio/mpeg",
		"video/webm; codecs=opus":  "video/webm",
		"image/svg+xml":            "audio/mpeg",
		"application/octet-stream": "audio/mpeg",
	} {
		assert.Equal(t, want, meetings.MediaType(declared, "audio/mpeg"), declared)
	}

	assert.Equal(t, "application/octet-stream", meetings.MediaType("audio/mpeg", "text/html; charset=utf-8"),
		"a page named a sound is not one")
}

// sendSlowly sends a request as a browser does over a slow link: its head at once, then its body in pieces of four
// bytes, a pause before each, and reads the answer.
func sendSlowly(t *testing.T, addr, head string, body []byte, pause time.Duration) (*http.Response, error) {
	t.Helper()

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := fmt.Fprintf(conn, "%sContent-Length: %d\r\n\r\n", head, len(body)); err != nil {
		return nil, err
	}

	for len(body) > 0 {
		time.Sleep(pause)

		n := min(4, len(body))
		if _, err := conn.Write(body[:n]); err != nil {
			return nil, err
		}

		body = body[n:]
	}

	return http.ReadResponse(bufio.NewReader(conn), nil)
}

// Scenario (issue #779): the client API is served with read and write timeouts, as the control plane is, shortened
// here; a recording whose body takes longer than both to arrive is still taken, as the owner's 6 MB recording over a
// slow link must be. Another request keeps the server's timeouts.
func TestARecordingSlowerThanTheServersTimeoutsIsTaken(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	work, err := meetings.New(meetings.Config{
		Dir: filepath.Join(t.TempDir(), "meetings"), Store: w.svc.Meetings(), Speech: twoVoices{}, Models: noModel{},
		Usage: noUsage{},
	})
	require.NoError(t, err)
	w.srv.WithMeetings(work)

	hs := httptest.NewUnstartedServer(w.mux)
	hs.Config.ReadTimeout = 300 * time.Millisecond
	hs.Config.WriteTimeout = 300 * time.Millisecond
	hs.Start()
	t.Cleanup(hs.Close)

	_, secret, err := w.svc.AddToken(ctx, "web", bearertoken.Owner)
	require.NoError(t, err)

	addr := strings.TrimPrefix(hs.URL, "http://")
	head := func(path, contentType string) string {
		return "POST " + BasePath + path + " HTTP/1.1\r\nHost: " + addr + "\r\nAuthorization: Bearer " + secret +
			"\r\nContent-Type: " + contentType + "\r\n"
	}

	// 16 bytes in four pieces, 200 ms apart: 800 ms, past both timeouts.
	resp, err := sendSlowly(t, addr, head("/meetings?file_name=sync.mp3", "application/octet-stream"),
		[]byte("ID3-0123456789ab"), 200*time.Millisecond)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	other, err := sendSlowly(t, addr, head("/runs", "application/json"),
		[]byte(`{"objective":"x","repo":"a/b"}`), 200*time.Millisecond)
	if err == nil {
		defer func() { _ = other.Body.Close() }()
		assert.NotEqual(t, http.StatusCreated, other.StatusCode, "another request keeps the server's timeouts")
	}
}
