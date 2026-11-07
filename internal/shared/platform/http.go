package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Probe paths. /livez answers while the process runs; /readyz while what it
// depends on answers too.
const (
	PathLive  = "/livez"
	PathReady = "/readyz"
)

const (
	readyTimeout    = 2 * time.Second
	shutdownTimeout = 20 * time.Second
)

// RegisterHealth adds the probes to mux. ready reports whether the process can
// serve; nil means it can.
func RegisterHealth(mux *http.ServeMux, ready func(context.Context) error, logger *slog.Logger) {
	mux.HandleFunc("GET "+PathLive, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET "+PathReady, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()

		if err := ready(ctx); err != nil {
			logger.WarnContext(ctx, "not ready",
				slog.String("event", "platform.not_ready"),
				slog.String("error", err.Error()),
			)
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

// Serve runs an HTTP server on ln until ctx is canceled, then stops taking
// requests and lets those in flight finish within the shutdown timeout.
func Serve(ctx context.Context, ln net.Listener, handler http.Handler, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	errc := make(chan error, 1)

	go func() { errc <- srv.Serve(ln) }()

	logger.InfoContext(ctx, "server started",
		slog.String("event", "platform.server.started"),
		slog.String("addr", ln.Addr().String()),
	)

	select {
	case err := <-errc:
		return fmt.Errorf("serving HTTP: %w", err)
	case <-ctx.Done():
	}

	logger.InfoContext(ctx, "server stopping", slog.String("event", "platform.server.stopping"))

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down HTTP: %w", err)
	}

	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving HTTP: %w", err)
	}

	return nil
}
