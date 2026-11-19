package clientapi

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// A meeting recorded live from the browser (issue #820), as MemoryOS records one track
// (memoryos-api/.../meeting/MeetingStreamWebSocketHandler.java, MeetingHandshakeInterceptor.java; docs/specs/meeting.md
// "Recording a track"): the owner asks a ticket for a track with an authorized request, as a terminal's is asked
// (terminal.go), then opens a WebSocket the ticket names. The browser sends PCM16 24 kHz mono in binary frames and one
// {"type":"end"}; the control plane relays the audio to the speech provider, so the provider's key never reaches the
// browser, and answers ready, preview, line, finished and error {code}. It pings the socket every 20 seconds, as a
// proxy reads an idle upstream as a dead one, and ends a track that sends no audio for 60 seconds.

const (
	// meetingTicketFor is how long a track's ticket waits for its WebSocket (MemoryOS's 60 seconds).
	meetingTicketFor = 60 * time.Second
	// meetingFrameBytes bounds one frame from the browser (MemoryOS's MAX_BINARY_FRAME).
	meetingFrameBytes = 64 << 10
	meetingIdle       = 60 * time.Second
	meetingPing       = 20 * time.Second
	meetingWatch      = 5 * time.Second
)

// The codes a track's socket ends with (MemoryOS's MEETING_* codes, in this API's words).
const (
	trackBusy           = "busy"
	trackEnded          = "ended"
	trackInvalid        = "invalid"
	trackInvalidAudio   = "invalid_audio"
	trackInvalidMessage = "invalid_message"
	trackTooLong        = "too_long"
	trackIdle           = "idle"
	trackProviderFailed = "provider_failed"
	trackUnavailable    = "unavailable"
)

// meetingTicket is one permission to record a track of a meeting.
type meetingTicket struct {
	meetingID string
	track     string
	by        string
	from      string
	expires   time.Time
}

// meetingTickets are the tickets not used yet.
type meetingTickets struct {
	mu      sync.Mutex
	tickets map[string]meetingTicket
}

func (t *meetingTickets) add(ticket meetingTicket) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("making a track's ticket: %w", err)
	}

	id := hex.EncodeToString(raw)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.tickets == nil {
		t.tickets = map[string]meetingTicket{}
	}

	now := time.Now()
	for old, tk := range t.tickets {
		if now.After(tk.expires) {
			delete(t.tickets, old)
		}
	}

	t.tickets[id] = ticket

	return id, nil
}

// take returns a ticket once, if it has not expired.
func (t *meetingTickets) take(id string) (meetingTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ticket, ok := t.tickets[id]
	delete(t.tickets, id)

	return ticket, ok && time.Now().Before(ticket.expires)
}

// StartLiveMeeting starts a meeting recording.
func (s *Server) StartLiveMeeting(ctx context.Context, req openapi.StartLiveMeetingRequestObject,
) (openapi.StartLiveMeetingResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	m, err := s.meetings.StartLive(ctx, meetings.LiveStart{
		Kind: string(req.Body.Kind), Title: val(req.Body.Title), Language: val(req.Body.Language),
		People: val(req.Body.People),
	})
	if err != nil {
		return nil, err
	}

	return openapi.StartLiveMeeting201JSONResponse(meetingOut(m)), nil
}

// OpenMeetingTrack gives the owner a ticket to record one track of a meeting being recorded.
func (s *Server) OpenMeetingTrack(ctx context.Context, req openapi.OpenMeetingTrackRequestObject,
) (openapi.OpenMeetingTrackResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	track := string(req.Body.Track)
	if _, err := s.meetings.Recordable(ctx, req.MeetingId, track); err != nil {
		return nil, err
	}

	expires := time.Now().Add(meetingTicketFor)

	ticket, err := s.meetingTickets.add(meetingTicket{
		meetingID: req.MeetingId, track: track, by: principal(ctx).Name, from: callerOf(ctx).address,
		expires: expires,
	})
	if err != nil {
		return nil, err
	}

	return openapi.OpenMeetingTrack201JSONResponse{Ticket: ticket, ExpiresAt: expires.UTC()}, nil
}

// EndLiveMeeting ends a meeting being recorded.
func (s *Server) EndLiveMeeting(ctx context.Context, req openapi.EndLiveMeetingRequestObject,
) (openapi.EndLiveMeetingResponseObject, error) {
	if s.meetings == nil {
		return nil, errNoMeetings
	}

	m, err := s.meetings.End(ctx, req.MeetingId)
	if err != nil {
		return nil, err
	}

	return openapi.EndLiveMeeting200JSONResponse(meetingOut(m)), nil
}

// trackSocket writes a track's messages to the browser and closes it once.
type trackSocket struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
	once   sync.Once
}

func (t *trackSocket) send(ctx context.Context, message map[string]any) {
	body, err := json.Marshal(message)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if t.conn.Write(ctx, websocket.MessageText, body) != nil {
		t.cancel()
	}
}

// fail says why the track ends and closes the socket.
func (t *trackSocket) fail(ctx context.Context, code string) {
	t.once.Do(func() {
		t.send(ctx, map[string]any{"type": "error", "code": code})
		_ = t.conn.Close(websocket.StatusPolicyViolation, code)
		t.cancel()
	})
}

// finish says the last words are kept and closes the socket.
func (t *trackSocket) finish(ctx context.Context) {
	t.once.Do(func() {
		t.send(ctx, map[string]any{"type": "finished"})
		_ = t.conn.Close(websocket.StatusNormalClosure, "finished")
		t.cancel()
	})
}

// meetingSocket records one track of a meeting. The ticket in its path is the permission, bound to the meeting and
// the track; the origin must be the web client's own, which the WebSocket library checks against the request's
// host, as a terminal's.
func (s *Server) meetingSocket(w http.ResponseWriter, r *http.Request) {
	ticket, ok := s.meetingTickets.take(r.PathValue("ticket"))
	if !ok || s.meetings == nil || ticket.meetingID != r.PathValue("meeting_id") ||
		ticket.track != r.URL.Query().Get("track") {
		http.Error(w, "unknown or expired ticket for this track", http.StatusNotFound)

		return
	}

	offsetMs, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offsetMs < 0 {
		http.Error(w, "the offset is the meeting's time in milliseconds", http.StatusBadRequest)

		return
	}

	// A track records for hours: neither the server's read nor its write timeout applies (terminal.go).
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(meetingFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	sock := &trackSocket{conn: conn, cancel: cancel}
	who := []any{
		slog.String("meeting_id", ticket.meetingID), slog.String("track", ticket.track),
		slog.String("by", ticket.by), slog.String("from", ticket.from),
	}

	track, err := s.meetings.OpenTrack(ctx, ticket.meetingID, ticket.track, time.Duration(offsetMs)*time.Millisecond,
		meetings.TrackListener{
			Preview: func(speaker, text string) {
				sock.send(ctx, map[string]any{
					"type": "preview", "track": ticket.track, "speaker": speaker, "text": text,
				})
			},
			Line: func(l meetingstore.Line) {
				sock.send(ctx, map[string]any{"type": "line", "line": lineOut(l)})
			},
			Failed: func(err error) { sock.fail(ctx, trackCode(err)) },
		})
	if err != nil {
		s.logger.WarnContext(ctx, "a meeting's track was refused", append([]any{
			slog.String("event", "clientapi.meeting_track_refused"), slog.String("error", err.Error()),
		}, who...)...)
		sock.fail(ctx, trackCode(err))

		return
	}

	started := time.Now()

	s.logger.InfoContext(ctx, "a meeting's track opened",
		append([]any{slog.String("event", "clientapi.meeting_track_opened")}, who...)...)

	sock.send(ctx, map[string]any{"type": "ready"})
	s.relayTrack(ctx, sock, track)
	track.Close(ctx)

	s.logger.InfoContext(context.WithoutCancel(ctx), "a meeting's track closed", append([]any{
		slog.String("event", "clientapi.meeting_track_closed"),
		slog.Duration("lasted", time.Since(started).Round(time.Second)),
	}, who...)...)
}

// relayTrack copies the browser's audio to the track until the browser ends it, leaves, or the track ends.
func (s *Server) relayTrack(ctx context.Context, sock *trackSocket, track *meetings.Track) {
	var (
		mu        sync.Mutex
		lastAudio = time.Now()
		ending    = false
	)

	// The watch: no audio for meetingIdle ends the track, and a ping every meetingPing keeps each hop awake while
	// nobody speaks (MemoryOS's expire). A track the meeting's end finished says so.
	idleAfter := cmp.Or(s.trackIdle, meetingIdle)

	go func() {
		tick := time.NewTicker(min(meetingWatch, idleAfter/2))
		defer tick.Stop()

		pinged := time.Now()

		for {
			select {
			case <-ctx.Done():
				return
			case <-track.Done():
				sock.finish(ctx)

				return
			case <-tick.C:
			}

			mu.Lock()
			idle, done := time.Since(lastAudio) > idleAfter, ending
			mu.Unlock()

			if idle && !done {
				sock.fail(ctx, trackIdle)

				return
			}

			if time.Since(pinged) >= meetingPing {
				pinged = time.Now()

				go func() {
					pctx, cancel := context.WithTimeout(ctx, meetingPing)
					defer cancel()

					_ = sock.conn.Ping(pctx)
				}()
			}
		}
	}()

	for {
		typ, data, err := sock.conn.Read(ctx)
		if err != nil {
			return
		}

		if typ == websocket.MessageBinary {
			if len(data)%2 != 0 {
				sock.fail(ctx, trackInvalidAudio)

				return
			}

			if err := track.Append(data); err != nil {
				sock.fail(ctx, trackCode(err))

				return
			}

			mu.Lock()
			lastAudio = time.Now()
			mu.Unlock()

			continue
		}

		var m struct {
			Type string `json:"type"`
		}

		if json.Unmarshal(data, &m) != nil || m.Type != "end" {
			sock.fail(ctx, trackInvalidMessage)

			return
		}

		mu.Lock()
		ending = true
		mu.Unlock()

		// The last words are kept before finished is said; the browser waits for it.
		track.Finish(ctx)
		sock.finish(ctx)

		return
	}
}

// trackCode is the code a track's socket ends with for an error.
func trackCode(err error) string {
	switch {
	case errors.Is(err, meetings.ErrBusy):
		return trackBusy
	case errors.Is(err, meetings.ErrTooLong):
		return trackTooLong
	case errors.Is(err, meetings.ErrNotRecording), errors.Is(err, orchestration.ErrNotFound):
		return trackEnded
	case errors.Is(err, speech.ErrUnavailable):
		return trackUnavailable
	case errors.Is(err, orchestration.ErrInvalid), errors.Is(err, speech.ErrInvalid):
		return trackInvalid
	default:
		return trackProviderFailed
	}
}
