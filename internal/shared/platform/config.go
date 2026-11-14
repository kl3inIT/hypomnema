// Package platform holds the technical code every deployable shares: reading
// configuration, logging, serving HTTP with health probes, and the version.
// It depends on no tree of the repository and on no driver, because it is
// linked into every binary.
package platform

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Log formats.
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// ErrMissing reports a required setting that is not set.
var ErrMissing = errors.New("required setting is not set")

// Env reads settings from an environment, such as os.Getenv, and collects
// every problem so a misconfigured process reports all of them at once.
type Env struct {
	getenv func(string) string
	errs   []error
}

// NewEnv reads settings through getenv.
func NewEnv(getenv func(string) string) *Env {
	return &Env{getenv: getenv}
}

// String returns the setting, or def when it is unset or blank.
func (e *Env) String(name, def string) string {
	if v := strings.TrimSpace(e.getenv(name)); v != "" {
		return v
	}

	return def
}

// Required returns the setting and records an error when it is unset.
func (e *Env) Required(name string) string {
	v := strings.TrimSpace(e.getenv(name))
	if v == "" {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", name, ErrMissing))
	}

	return v
}

// Log reads the logging settings named prefix_LOG_LEVEL and prefix_LOG_FORMAT.
func (e *Env) Log(prefix string) LogConfig {
	cfg := LogConfig{Format: e.String(prefix+"_LOG_FORMAT", LogFormatJSON)}

	if cfg.Format != LogFormatJSON && cfg.Format != LogFormatText {
		e.errs = append(e.errs, fmt.Errorf("%s_LOG_FORMAT: %q is neither %q nor %q",
			prefix, cfg.Format, LogFormatJSON, LogFormatText))
	}

	if err := cfg.Level.UnmarshalText([]byte(e.String(prefix+"_LOG_LEVEL", "info"))); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s_LOG_LEVEL: %w", prefix, err))
	}

	return cfg
}

// Err returns every problem found so far, or nil.
func (e *Env) Err() error {
	return errors.Join(e.errs...)
}

// LogConfig is how a process logs.
type LogConfig struct {
	Level  slog.Level
	Format string
}
