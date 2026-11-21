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
// turns and how many agents' tool calls (issue #1060) run now, so that the update waits for them before it stops the service. It is Hermes's gateway control
// socket (gateway/control_socket.py): a local socket in the data folder, never a TCP port, whose file permissions
// are the only check; a stale one is removed before listening, and the socket is the control plane's user's alone.
// GET selfupdate.PathTurns and POST selfupdate.PathUpdate are served.

// controlSocket is where the control plane listens: next to its database, the folder `hypomnema update` knows.
func controlSocket(database string) string {
	return filepath.Join(filepath.Dir(database), selfupdate.ControlSocket)
}

// serveControl serves the control socket at path until ctx ends. A socket that cannot listen is logged, and the
// control plane serves without it: an update then does not wait (selfupdate).
func serveControl(ctx context.Context, path string, working func() []string, calls func() int,
	begin func(mark func() error) error, logger *slog.Logger,
) {
	fail := func(err error) {
		logger.ErrorContext(ctx, "control socket cannot listen", slog.String("event", "controlplane.control.failed"),
			slog.String("path", path), slog.String("error", err.Error()))
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fail(err)

		return
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		fail(err)

		return
	}

	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()

		fail(err)

		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+selfupdate.PathTurns, func(w http.ResponseWriter, _ *http.Request) {
		chats := working()
		if chats == nil {
			chats = []string{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(selfupdate.Turns{Chats: chats, ToolCalls: calls()})
	})

	// The update names itself here, under the lock of pi's starts (coordinator BeginUpdate): no pi starts after.
	mux.HandleFunc("POST "+selfupdate.PathUpdate, func(w http.ResponseWriter, r *http.Request) {
		var b selfupdate.Begin
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&b); err != nil {
			http.Error(w, "an update names its process", http.StatusBadRequest)

			return
		}

		if err := begin(func() error { return selfupdate.WriteMarker(filepath.Dir(path), b) }); err != nil {
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
