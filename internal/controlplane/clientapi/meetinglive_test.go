package clientapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech/speechtest"
)

// liveMeetings gives the API's world meetings whose speech streams to a fake Soniox realtime API that hears one line
// in the first half second of each stream.
func liveMeetings(t *testing.T, w *world) {
	t.Helper()

	var mu sync.Mutex

	answered := map[int]bool{}
	fake := &speechtest.Realtime{
		Answer: func(stream int, _ map[string]any, received int) ([]string, bool) {
			mu.Lock()
			defer mu.Unlock()

			if answered[stream] || received < speech.LiveBytesPerSecond/2 {
				return nil, false
			}

			answered[stream] = true

			return []string{speechtest.Tokens(true, true,
				speechtest.Token{Text: "Shall we start?", Start: 100, End: 400, Speaker: "1"})}, false
		},
	}
	voice := speech.New(speech.Config{
		Keys: func(context.Context) ([]speech.Key, error) {
			return []speech.Key{{Provider: speech.ProviderSoniox, Name: "speech-soniox", Value: "sk"}}, nil
		},
		SonioxRealtimeURL: fake.Serve(t),
	})

	work, err := meetings.New(meetings.Config{
		Dir: filepath.Join(t.TempDir(), "meetings"), Store: w.svc.Meetings(), Speech: voice, Models: noModel{},
		Usage: noUsage{},
	})
	require.NoError(t, err)
	w.srv.WithMeetings(work)
}

// trackURL is a track's WebSocket.
func (w *world) trackURL(meetingID, ticket, track string, offset int) string {
	return "ws" + strings.TrimPrefix(w.url, "http") + BasePath + "/meetings/" + meetingID + "/stream/" + ticket +
		"?track=" + track + "&offset=" + strconv.Itoa(offset)
}

// next reads the socket's next message as JSON.
func next(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))

	return m
}

// until reads messages until one of a type.
func until(t *testing.T, conn *websocket.Conn, typ string) map[string]any {
	t.Helper()

	for {
		m := next(t, conn)
		if m["type"] == typ {
			return m
		}

		require.NotEqual(t, "error", m["type"], "an error before %s: %v", typ, m)
	}
}

// Scenario: the owner records a meeting in person over the API. Only the owner starts one and asks a track's ticket;
// the ticket opens the track's WebSocket once, for its own meeting and track; the audio sent is heard and its line is
// sent back as kept; the end is answered finished after the last words; a second socket for an open track is
// refused busy; a track that sends nothing is ended idle; ending the meeting answers it transcribed.
func TestTheOwnerRecordsAMeetingLiveOverTheAPI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	liveMeetings(t, w)
	w.srv.trackIdle = 500 * time.Millisecond

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	refused, err := pi.StartLiveMeetingWithResponse(ctx, openapi.NewLiveMeeting{Kind: openapi.InPerson})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "only the owner records")

	started, err := owner.StartLiveMeetingWithResponse(ctx, openapi.NewLiveMeeting{
		Kind: openapi.InPerson, Language: ptr("en"), People: &[]string{"Kai"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, started.StatusCode(), string(started.Body))

	m := started.JSON201
	assert.Equal(t, openapi.MeetingStateRecording, m.State)
	assert.Equal(t, openapi.Live, m.Source)
	assert.Equal(t, openapi.InPerson, *m.Kind)

	again, err := owner.StartLiveMeetingWithResponse(ctx, openapi.NewLiveMeeting{Kind: openapi.Online})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, again.StatusCode(), "one meeting records at a time")

	piTicket, err := pi.OpenMeetingTrackWithResponse(ctx, m.Id, openapi.MeetingTrackRequest{
		Track: openapi.MeetingLiveTrackMic,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, piTicket.StatusCode())

	tab, err := owner.OpenMeetingTrackWithResponse(ctx, m.Id, openapi.MeetingTrackRequest{
		Track: openapi.MeetingLiveTrackTab,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, tab.StatusCode(), "in person, the microphone only")

	ticket := func() string {
		resp, err := owner.OpenMeetingTrackWithResponse(ctx, m.Id, openapi.MeetingTrackRequest{
			Track: openapi.MeetingLiveTrackMic,
		})
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))

		return resp.JSON201.Ticket
	}

	dial := func(url string) (*websocket.Conn, int) {
		conn, resp, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose // the library closes the handshake's body
		if err != nil {
			require.NotNil(t, resp, err)

			return nil, resp.StatusCode
		}

		t.Cleanup(func() { _ = conn.CloseNow() })

		return conn, http.StatusSwitchingProtocols
	}

	other := ticket()
	_, status := dial(w.trackURL("mtg_other", other, "mic", 0))
	assert.Equal(t, http.StatusNotFound, status, "a ticket is for its own meeting")
	_, status = dial(w.trackURL(m.Id, other, "mic", 0))
	assert.Equal(t, http.StatusNotFound, status, "a ticket works once")

	used := ticket()
	conn, status := dial(w.trackURL(m.Id, used, "mic", 0))
	require.Equal(t, http.StatusSwitchingProtocols, status)
	assert.Equal(t, "ready", next(t, conn)["type"])

	_, status = dial(w.trackURL(m.Id, used, "mic", 0))
	assert.Equal(t, http.StatusNotFound, status, "a ticket works once")

	busy, status := dial(w.trackURL(m.Id, ticket(), "mic", 0))
	require.Equal(t, http.StatusSwitchingProtocols, status)
	assert.Equal(t, map[string]any{"type": "error", "code": "busy"}, next(t, busy), "a second socket for the track")

	require.NoError(t, conn.Write(ctx, websocket.MessageBinary, make([]byte, speech.LiveBytesPerSecond/2)))

	line := until(t, conn, "line")["line"].(map[string]any) //nolint:forcetypeassert // the test fails on a panic
	assert.Equal(t, "Shall we start?", line["text"])
	assert.Equal(t, "mic", line["track"])
	assert.InDelta(t, 100, line["start_ms"], 0)

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"end"}`)))
	assert.Equal(t, "finished", until(t, conn, "finished")["type"])

	idle, status := dial(w.trackURL(m.Id, ticket(), "mic", 500))
	require.Equal(t, http.StatusSwitchingProtocols, status)
	assert.Equal(t, "ready", next(t, idle)["type"])
	assert.Equal(t, map[string]any{"type": "error", "code": "idle"}, until(t, idle, "error"))

	ended, err := owner.EndLiveMeetingWithResponse(ctx, m.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ended.StatusCode(), string(ended.Body))
	assert.Equal(t, openapi.MeetingStateTranscribed, ended.JSON200.State)
	assert.Equal(t, openapi.MeetingNotesStateWriting, ended.JSON200.Notes.State)

	got, err := pi.GetMeetingWithResponse(ctx, m.Id)
	require.NoError(t, err)
	require.Len(t, got.JSON200.Lines, 1)
	assert.Equal(t, openapi.MeetingTrackMic, got.JSON200.Lines[0].Track)

	late, err := owner.OpenMeetingTrackWithResponse(ctx, m.Id, openapi.MeetingTrackRequest{
		Track: openapi.MeetingLiveTrackMic,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, late.StatusCode(), "an ended meeting records no more")
}
