package agentapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// maxBackgroundReport bounds a report: 2,000 characters of output are at most 8 KB, and a path.
const maxBackgroundReport = 16 << 10

// background takes a Bot's pi's reports of its background commands (issue #1058, orchestration/background.go): a
// command started is kept, one that ended wakes the chat. Only a pi's own token reports, for its own chat; a
// command is its pi's, named by the token's hash, so a pi that follows in the chat cannot end another's.
func (s *Server) background(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if !a.isBotPi() || a.readRun {
		refuse(w, http.StatusForbidden, "only a Bot's pi in its chat reports background commands")

		return
	}

	var asked agenttool.BackgroundReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBackgroundReport)).Decode(&asked); err != nil {
		refuse(w, http.StatusBadRequest, "send {kind, id, ...} as JSON")

		return
	}

	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	pi := bearertoken.Hash(secret)

	switch asked.Kind {
	case agenttool.BackgroundStarted:
		err = s.store.StartBackground(r.Context(), a.chatID, pi, asked.ID)
	case agenttool.BackgroundEnded:
		_, err = s.store.EndBackground(r.Context(), a.chatID, pi, orchestration.BackgroundEnd{
			ID: asked.ID, ExitCode: asked.ExitCode, Signal: asked.Signal, TimedOut: asked.TimedOut,
			Output: asked.Output, OutputChars: asked.OutputChars, OutputFile: asked.OutputFile, Dropped: asked.Dropped,
		})
	default:
		refuse(w, http.StatusBadRequest, "kind is started or ended")

		return
	}

	if errors.Is(err, orchestration.ErrInvalid) {
		refuse(w, http.StatusUnprocessableEntity, err.Error())

		return
	}

	if err != nil {
		s.fail(w, r, err)

		return
	}

	s.logger.InfoContext(r.Context(), "a Bot's background command", slog.String("event", "agentapi.background"),
		slog.String("agent", a.name), slog.String("kind", asked.Kind), slog.String("id", asked.ID))
	answer(w, map[string]bool{"ok": true})
}
