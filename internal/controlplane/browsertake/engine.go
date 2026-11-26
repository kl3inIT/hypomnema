package browsertake

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
)

// Engine is the shared browser as a Recorder uses it.
func Engine(e *browser.Engine) Browser { return engine{e} }

type engine struct{ e *browser.Engine }

func (g engine) Film(ctx context.Context, agent string, viewport browser.Size, frame func(browser.Frame),
) (Film, error) {
	f, err := g.e.Film(ctx, agent, viewport, frame)
	if err != nil {
		return nil, err
	}

	return f, nil
}

func (g engine) Box(ctx context.Context, agent, selector string) (browser.Box, error) {
	return g.e.Box(ctx, agent, selector)
}
