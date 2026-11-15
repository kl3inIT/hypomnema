package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The phone a node holds (issue #417, step 2), given as the shared browser
// is (#212): an agent the browser's routes take is taken here, by the same
// check, with no setting and no Connection (owner, 2026-11-15). The routes
// have the browser's shape: a list of MCP tools, and a call whose answer is
// the tool's result. A worker's tools server and a Bot's (`hypomnema mcp`,
// issue #536 step 4) list them here beside PathTools.

// Device is the phone: its tools, and one call of a tool for the agent named.
type Device interface {
	Tools(ctx context.Context) ([]devicetool.Tool, error)
	Call(ctx context.Context, agent, tool string, arguments json.RawMessage) (json.RawMessage, error)
}

// WithDevice gives agents the phone. Without it the phone's routes answer
// that there is none.
func (s *Server) WithDevice(d Device) *Server {
	s.device = d

	return s
}

// maxDeviceCallBytes bounds a phone call's body: the text a tool types is
// the largest part of one.
const maxDeviceCallBytes = 64 << 10

func (s *Server) deviceTools(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if s.device == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no phone")

		return
	}

	tools, err := s.device.Tools(r.Context())
	if err != nil {
		s.failDevice(w, r, err)

		return
	}

	out := agenttool.BrowserToolList{Data: make([]agenttool.BrowserTool, 0, len(tools))}
	for _, t := range tools {
		out.Data = append(out.Data, agenttool.BrowserTool{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		})
	}

	answer(w, out)
}

func (s *Server) deviceCall(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	var asked agenttool.BrowserCall
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDeviceCallBytes)).Decode(&asked); err != nil ||
		asked.Tool == "" {
		refuse(w, http.StatusBadRequest, "name the tool and give its arguments as a JSON object")

		return
	}

	if s.device == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no phone")

		return
	}

	// A call may wait for the phone's machine to wake and the phone to boot, longer than the server's write
	// timeout; its answer must still reach the agent, which would otherwise repeat a tap it did not see.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	result, err := s.device.Call(r.Context(), a.key, asked.Tool, asked.Arguments)
	if err != nil {
		s.failDevice(w, r, err)

		return
	}

	// Which agent used which tool, and never what it typed or saw.
	s.logger.InfoContext(r.Context(), "an agent used the phone",
		slog.String("event", "agentapi.device_call"), slog.String("agent", a.name),
		slog.String("tool", asked.Tool))

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

// failDevice answers an error of the phone, as failBrowser does the browser's.
func (s *Server) failDevice(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, devicetool.ErrNoSuchTool):
		refuse(w, http.StatusNotFound, err.Error())
	case errors.Is(err, devicetool.ErrBadArguments):
		refuse(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, phone.ErrUnavailable):
		s.logger.WarnContext(r.Context(), "the phone was not available",
			slog.String("event", "agentapi.device_unavailable"), slog.String("error", err.Error()))
		refuse(w, http.StatusServiceUnavailable, err.Error())
	default:
		s.fail(w, r, err)
	}
}
