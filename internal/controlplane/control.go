package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
)

// The control socket (issue #716) answers `hypomnema update`, which runs as root on the same machine, which Bots'
// turns, agents' tool calls (issue #1060), and video renders (issue #1328) run now, so the update can wait for them.
// It is Hermes's gateway control socket (gateway/control_socket.py): a local socket in the data folder, never a TCP
// port, whose file permissions
// are the only check; a stale one is removed before listening, and the socket is the control plane's user's alone.
// GET selfupdate.PathTurns and POST selfupdate.PathUpdate are served.

// controlSocket is where the control plane listens: next to its database, the folder `hypomnema update` knows.
func controlSocket(database string) string {
	return filepath.Join(filepath.Dir(database), selfupdate.ControlSocket)
}

// listenControl replaces a stale socket and returns a listener restricted to the control plane's user.
func listenControl(ctx context.Context, path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()

		return nil, err
	}

	return ln, nil
}

// serveControl handles requests on the open control socket until ctx ends.
func serveControl(ctx context.Context, ln net.Listener, working func() []string, calls, renders func() int,
	begin func(mark func() error) error, logger *slog.Logger,
) {
	dir := filepath.Dir(ln.Addr().String())
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+selfupdate.PathTurns, func(w http.ResponseWriter, _ *http.Request) {
		chats := working()
		if chats == nil {
			chats = []string{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(selfupdate.Turns{Chats: chats, ToolCalls: calls(), Renders: renders()})
	})

	// The update names itself here, under the lock of pi's starts (coordinator BeginUpdate): no pi starts after.
	mux.HandleFunc("POST "+selfupdate.PathUpdate, func(w http.ResponseWriter, r *http.Request) {
		var b selfupdate.Begin
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil {
			http.Error(w, "an update names its process", http.StatusBadRequest)

			return
		}

		if err := begin(func() error { return selfupdate.WriteMarker(dir, b) }); err != nil {
			logger.ErrorContext(r.Context(), "marking the update failed", slog.String("event", "controlplane.control.failed"),
				slog.String("error", err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		logger.InfoContext(r.Context(), "an update began", slog.String("event", "controlplane.update.began"),
			slog.Int("pid", b.PID))
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.ErrorContext(ctx, "control socket stopped", slog.String("event", "controlplane.control.failed"),
			slog.String("error", err.Error()))
	}
}
