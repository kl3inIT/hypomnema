package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// profilerHandler serves Go's profiles (https://pkg.go.dev/net/http/pprof)
// on a mux of its own, never on the control plane's: the package's import
// registers them on http.DefaultServeMux, which nothing here serves, and a
// custom mux takes its handlers as the package documents.
func profilerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return mux
}

// loopbackAddr checks that addr is host:port with a loopback IP for its
// host, so the profiler is reached from this machine only (issue #586).
func loopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not host:port: %w", addr, err)
	}

	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%q is not a loopback address, such as 127.0.0.1:6060", addr)
	}

	return nil
}

// serveProfiler serves the profiles on ln until ctx ends. Its server has no
// write timeout, which would cut a CPU profile longer than it short
// (net/http/pprof refuses one that would not fit).
func serveProfiler(ctx context.Context, ln net.Listener, logger *slog.Logger) {
	srv := &http.Server{
		Handler:           profilerHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	logger.InfoContext(ctx, "profiler started",
		slog.String("event", "controlplane.pprof.started"), slog.String("addr", ln.Addr().String()))

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.ErrorContext(ctx, "profiler stopped",
			slog.String("event", "controlplane.pprof.failed"), slog.String("error", err.Error()))
	}
}
