package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The Social listening page of Settings → Gateway (issue #1290): each source with its group, its switch, its
// backends in order with their rests, its last success, its items this week and its last error with the one step
// that fixes it (internal/controlplane/listening, health.go), as the Web search page lists its providers
// (web_search.go). Only the owner sees and changes it, as the Muse page.

// ListeningSettings is social listening's sources and what the owner sets of them.
type ListeningSettings interface {
	States(ctx context.Context) ([]listening.SourceState, error)
	SetSource(ctx context.Context, source string, on bool) error
	SetOrder(ctx context.Context, source string, backends []string) error
	// Test reads a source with its live test now, its rests cleared.
	Test(ctx context.Context, source string) error
}

// WithListening gives the API social listening's sources.
func (s *Server) WithListening(l ListeningSettings) *Server {
	s.listening = l

	return s
}

var errNoListening = fmt.Errorf("%w: this control plane has no social listening", orchestration.ErrUnavailable)

// GetListening answers the sources.
func (s *Server) GetListening(ctx context.Context, _ openapi.GetListeningRequestObject,
) (openapi.GetListeningResponseObject, error) {
	out, err := s.listeningSources(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetListening200JSONResponse(out), nil
}

// SetListeningSource switches a source on or off and answers the sources.
func (s *Server) SetListeningSource(ctx context.Context, req openapi.SetListeningSourceRequestObject,
) (openapi.SetListeningSourceResponseObject, error) {
	if s.listening == nil {
		return nil, errNoListening
	}

	if err := s.listening.SetSource(ctx, req.Source, req.Body.On); err != nil {
		return nil, listeningError(err)
	}

	out, err := s.listeningSources(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetListeningSource200JSONResponse(out), nil
}

// SetListeningOrder keeps a source's backends' order and answers the sources.
func (s *Server) SetListeningOrder(ctx context.Context, req openapi.SetListeningOrderRequestObject,
) (openapi.SetListeningOrderResponseObject, error) {
	if s.listening == nil {
		return nil, errNoListening
	}

	if err := s.listening.SetOrder(ctx, req.Source, req.Body.Backends); err != nil {
		return nil, listeningError(err)
	}

	out, err := s.listeningSources(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetListeningOrder200JSONResponse(out), nil
}

// TestListeningSource tests a source now and answers the sources.
func (s *Server) TestListeningSource(ctx context.Context, req openapi.TestListeningSourceRequestObject,
) (openapi.TestListeningSourceResponseObject, error) {
	if s.listening == nil {
		return nil, errNoListening
	}

	if err := s.listening.Test(ctx, req.Source); err != nil {
		return nil, listeningError(err)
	}

	out, err := s.listeningSources(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.TestListeningSource200JSONResponse(out), nil
}

func listeningError(err error) error {
	switch {
	case errors.Is(err, listening.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	case errors.Is(err, listening.ErrNotFound):
		return fmt.Errorf("%w: %w", orchestration.ErrNotFound, err)
	default:
		return err
	}
}

func (s *Server) listeningSources(ctx context.Context) (openapi.Listening, error) {
	if s.listening == nil {
		return openapi.Listening{}, errNoListening
	}

	states, err := s.listening.States(ctx)
	if err != nil {
		return openapi.Listening{}, err
	}

	out := openapi.Listening{Sources: make([]openapi.ListeningSource, 0, len(states))}

	for _, st := range states {
		src := openapi.ListeningSource{
			Name: st.Name, Label: st.Label, Adds: st.Adds, On: st.On, Group: openapi.ListeningGroup(st.Group),
			Regressed: st.Regressed, ItemsThisWeek: st.ItemsThisWeek, CheckedAt: timePtr(st.CheckedAt),
			LastSuccessAt: timePtr(st.LastSuccess), LastError: toListeningFailure(st.LastError),
			Backends: make([]openapi.ListeningBackend, 0, len(st.Backends)),
		}

		if st.Page != "" {
			src.Page = &st.Page
		}

		if st.Code != "" {
			code := string(st.Code)
			src.Code = &code
		}

		for _, b := range st.Backends {
			backend := openapi.ListeningBackend{
				Name: b.Name, OptIn: b.OptIn, State: openapi.ListeningBackendStateReady, LastOkAt: timePtr(b.LastOK),
				LastError: toListeningFailure(b.LastError),
			}

			if b.Resting != nil {
				code := string(b.Resting.Code)
				backend.State = openapi.ListeningBackendStateResting
				backend.RestingUntil = timePtr(b.Resting.Until)
				backend.RestCode = &code
			}

			src.Backends = append(src.Backends, backend)
		}

		switch st.Group {
		case listening.GroupWorking:
			out.Counts.Working++
		case listening.GroupUnverified:
			out.Counts.Unverified++
		case listening.GroupBroken:
			out.Counts.Broken++
		case listening.GroupCouldBeOn:
			out.Counts.CouldBeOn++
		}

		out.Sources = append(out.Sources, src)
	}

	return out, nil
}

func toListeningFailure(f *listening.Failure) *openapi.ListeningFailure {
	if f == nil {
		return nil
	}

	out := &openapi.ListeningFailure{Code: string(f.Code), Detail: f.Detail, At: f.At.UTC(), Fix: f.Fix}
	if f.Backend != "" {
		out.Backend = &f.Backend
	}

	return out
}
