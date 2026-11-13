package agentapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// maxMemoryChangeBytes bounds a change: an entry is far shorter than a memory.
const maxMemoryChangeBytes = 64 << 10

// memory changes a worker's memory (issue #21): the memory of the agent its
// Dispatch runs as, or the one about the owner. Only a worker's own key may:
// a Bot changes its memory through the client API, and the owner edits any
// memory there. The answer says how full the memory is, for the model only.
func (s *Server) memory(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if a.dispatchID == "" {
		refuse(w, http.StatusForbidden, "only a worker changes its memory here")

		return
	}

	var asked agenttool.MemoryChange
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMemoryChangeBytes)).Decode(&asked); err != nil ||
		(asked.Target != agenttool.MemoryAgent && asked.Target != agenttool.MemoryOwner) {
		refuse(w, http.StatusBadRequest, "say the target, agent or owner, and the change")

		return
	}

	m, err := s.store.ChangeDispatchMemory(r.Context(), a.dispatchID, asked.Target == agenttool.MemoryOwner,
		orchestration.MemoryOp{Kind: asked.Action, Text: asked.Text, Match: asked.Match})
	if err != nil {
		if errors.Is(err, orchestration.ErrInvalid) || errors.Is(err, orchestration.ErrNotFound) {
			refuse(w, http.StatusUnprocessableEntity, err.Error())

			return
		}

		s.logger.ErrorContext(r.Context(), "a worker's memory could not be changed",
			slog.String("event", "agentapi.memory_failed"), slog.String("agent", a.name), slog.Any("err", err))
		refuse(w, http.StatusInternalServerError, "the memory could not be changed")

		return
	}

	// Which agent changed which memory, and never what it wrote.
	s.logger.InfoContext(r.Context(), "a worker changed a memory",
		slog.String("event", "agentapi.memory"), slog.String("agent", a.name), slog.String("target", asked.Target),
		slog.String("action", asked.Action))

	w.Header().Set("Cache-Control", "no-store")
	answer(w, agenttool.MemoryAnswer{Memory: m.Text, Used: utf8.RuneCountInString(m.Text), Limit: m.Limit})
}
