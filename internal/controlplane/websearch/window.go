package websearch

import (
	"context"
	"strings"
	"time"
)

// WindowBasis describes how the provider that answered was asked to enforce the window.
type WindowBasis string

const (
	WindowNone         WindowBasis = "none"
	WindowQueryHint    WindowBasis = "query_hint"
	WindowServerBounds WindowBasis = "server_bounds"
)

type window struct {
	from, to string
}

type windowKey struct{}

// WithWindow asks searches made with ctx to use the inclusive UTC publication days from through to.
func WithWindow(ctx context.Context, from, to time.Time) context.Context {
	return context.WithValue(ctx, windowKey{}, window{
		from: from.UTC().Format(time.DateOnly),
		to:   to.UTC().Format(time.DateOnly),
	})
}

func searchWindow(ctx context.Context) window {
	w, _ := ctx.Value(windowKey{}).(window)

	return w
}

func (w window) hint(query string) (string, WindowBasis) {
	if w.from == "" {
		return query, WindowNone
	}

	// TrendRadar's DeepSeek provider keeps dates already supplied in the query.
	if !strings.Contains(strings.ToLower(query), "after:") {
		query += " after:" + w.from
	}

	return query, WindowQueryHint
}

func (w window) exaBounds(args map[string]any) WindowBasis {
	if w.from == "" {
		return WindowNone
	}

	args["startPublishedDate"] = w.from + "T00:00:00.000Z"
	args["endPublishedDate"] = w.to + "T23:59:59.999Z"

	return WindowServerBounds
}
