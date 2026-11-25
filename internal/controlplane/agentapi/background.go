package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// maxBackgroundRequest bounds a request: a command's text, at most 128 KiB, escaped as JSON, and a path.
const maxBackgroundRequest = 1 << 20

// Background runs a Bot's background commands (issue #1084): the coordinator's supervisor, which starts each as a
// systemd user unit that a restart of the control plane leaves running, and wakes the chat when it ends.
type Background interface {
	StartBackground(ctx context.Context, chatID, bot, pi string, in agenttool.BackgroundRequest,
	) (agenttool.BackgroundAnswer, error)
	StopBackground(ctx context.Context, chatID, id string) (bool, error)
}

// WithBackground gives a Bot's pi its background commands. Without it a background command is refused.
func (s *Server) WithBackground(b Background) *Server {
	s.bg = b

	return s
}

// background starts or stops a Bot's pi's background command (issues #1058, #1084, coordinator/background.go). Only
// a pi's own token asks, for its own chat. A command is its chat's, as it outlives the pi that started it: any pi of
// the chat may stop it.
func (s *Server) background(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if !a.isBotPi() || a.readRun {
		refuse(w, http.StatusForbidden, "only a Bot's pi in its chat runs background commands")

		return
	}

	var asked agenttool.BackgroundRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBackgroundRequest)).Decode(&asked); err != nil {
		refuse(w, http.StatusBadRequest, "send {kind, ...} as JSON")

		return
	}

	if s.bg == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane runs no background commands; run the command "+
			"without background")

		return
	}

	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	var got agenttool.BackgroundAnswer

	switch asked.Kind {
	case agenttool.BackgroundStart:
		got, err = s.bg.StartBackground(r.Context(), a.chatID, a.bot, bearertoken.Hash(secret), asked)
	case agenttool.BackgroundStop:
		got.ID = asked.ID
		got.Stopped, err = s.bg.StopBackground(r.Context(), a.chatID, asked.ID)
	default:
		refuse(w, http.StatusBadRequest, "kind is start or stop")

		return
	}

	switch {
	case errors.Is(err, orchestration.ErrInvalid):
		refuse(w, http.StatusUnprocessableEntity, err.Error())

		return
	case errors.Is(err, orchestration.ErrUnavailable):
		s.logger.WarnContext(r.Context(), "a background command could not run",
			slog.String("event", "agentapi.background_unavailable"), slog.String("error", err.Error()))
		refuse(w, http.StatusServiceUnavailable, err.Error())

		return
	case err != nil:
		s.fail(w, r, err)

		return
	}

	s.logger.InfoContext(r.Context(), "a Bot's background command", slog.String("event", "agentapi.background"),
		slog.String("agent", a.name), slog.String("kind", asked.Kind), slog.String("id", got.ID))
	answer(w, got)
}
