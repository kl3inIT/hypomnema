package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Events. A reader follows the log with a cursor (the seq of the last event
// it read). ListEvents can wait for the next event, so a coordinator outside
// the server waits without polling (OpenClaw's events_wait); the web reads a
// stream of Server-Sent Events that resumes with Last-Event-ID (Omnara).

// ListEvents returns the events after the cursor. With wait, an empty page
// waits up to that many seconds for an event to follow.
func (s *Server) ListEvents(ctx context.Context, req openapi.ListEventsRequestObject,
) (openapi.ListEventsResponseObject, error) {
	p := req.Params
	filter := orchestration.EventFilter{RunID: val(p.RunId), TaskID: val(p.TaskId)}

	var (
		wake     <-chan struct{}
		deadline <-chan time.Time
	)

	if wait := val(p.Wait); wait > 0 {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Subscribe before the first read, so an event committed in between still wakes the wait.
		wake = s.svc.Subscribe(ctx)
		timer := time.NewTimer(time.Duration(wait) * time.Second)

		defer timer.Stop()

		deadline = timer.C
	}

	for {
		page, err := s.svc.ListEvents(ctx, val(p.After), filter, val(p.Limit))
		if err != nil {
			return nil, err
		}

		if len(page.Events) > 0 || wake == nil {
			return eventList(page)
		}

		select {
		case <-wake:
		case <-deadline:
			return eventList(page)
		case <-s.stop:
			return eventList(page)
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for events: %w", ctx.Err())
		}
	}
}

func eventList(page orchestration.EventPage) (openapi.ListEvents200JSONResponse, error) {
	out := openapi.ListEvents200JSONResponse{
		Data: make([]openapi.Event, 0, len(page.Events)), NextAfter: page.NextAfter, HasMore: page.HasMore,
	}

	for _, e := range page.Events {
		event, err := toEvent(e)
		if err != nil {
			return openapi.ListEvents200JSONResponse{}, err
		}

		out.Data = append(out.Data, event)
	}

	return out, nil
}

// StreamEvents streams the events after the cursor until the client leaves
// or the server stops. Last-Event-ID wins over after, as a browser that
// reconnects sends it.
func (s *Server) StreamEvents(ctx context.Context, req openapi.StreamEventsRequestObject,
) (openapi.StreamEventsResponseObject, error) {
	p := req.Params

	after := val(p.After)
	if p.LastEventID != nil {
		after = *p.LastEventID
	}

	filter := orchestration.EventFilter{RunID: val(p.RunId), TaskID: val(p.TaskId)}

	return eventStream(func(w http.ResponseWriter) error {
		return s.stream(ctx, w, after, filter)
	}), nil
}

// eventStream writes the stream when the generated code visits it.
type eventStream func(w http.ResponseWriter) error

func (f eventStream) VisitStreamEventsResponse(w http.ResponseWriter) error { return f(w) }

func (s *Server) stream(ctx context.Context, w http.ResponseWriter, after int64, f orchestration.EventFilter) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("%w: streaming is not supported here", orchestration.ErrUnavailable)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	wake := s.svc.Subscribe(ctx)

	heartbeat := time.NewTicker(s.heartbeat)
	defer heartbeat.Stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if _, err := io.WriteString(w, ": ok\n\n"); err != nil {
		return nil
	}

	flusher.Flush()

	for {
		page, err := s.svc.ListEvents(ctx, after, f, orchestration.EventPageMax)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "event stream failed",
					slog.String("event", "clientapi.stream_failed"), slog.String("error", err.Error()))
			}

			return nil
		}

		for _, e := range page.Events {
			if !writeFrame(w, e) {
				return nil
			}

			after = e.Seq
		}

		flusher.Flush()

		if page.HasMore {
			continue
		}

	wait:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-s.stop:
				return nil
			case <-wake:
				break wait
			case <-heartbeat.C:
				if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
					return nil
				}

				flusher.Flush()
			}
		}
	}
}

// writeFrame writes one event: its seq as the id, its kind as the name, the
// Event as JSON. It reports whether the client is still there.
func writeFrame(w io.Writer, e orchestration.Event) bool {
	event, err := toEvent(e)
	if err != nil {
		return false
	}

	data, err := json.Marshal(event)
	if err != nil {
		return false
	}

	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", strconv.FormatInt(e.Seq, 10), e.Kind, data)

	return err == nil
}
