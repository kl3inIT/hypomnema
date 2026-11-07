package platform

import (
	"io"
	"log/slog"
)

// NewLogger returns a process's logger, writing to w (stdout in production):
// JSON for deployed processes, readable text for local development. Every
// record names the binary that wrote it.
func NewLogger(w io.Writer, cfg LogConfig, binary string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}

	var h slog.Handler = slog.NewJSONHandler(w, opts)
	if cfg.Format == LogFormatText {
		h = slog.NewTextHandler(w, opts)
	}

	return slog.New(h).With(slog.String("binary", binary), slog.String("version", Version))
}
