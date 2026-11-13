//go:build unix

package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func (o Options) binary() string   { return filepath.Join(o.Home, "bin", "hypomnema") }
func (o Options) database() string { return filepath.Join(o.Home, "data", "hypomnema.db") }

func (o Options) manifestURL() string {
	return strings.TrimSuffix(o.ReleaseURL, "/") + "/controlplane-" + runtime.GOOS + "-" + runtime.GOARCH + ".txt"
}

func (o Options) printf(format string, args ...any) {
	if o.Out != nil {
		_, _ = fmt.Fprintf(o.Out, format, args...)
	}
}

// waitReady polls Ready until it answers 200 or ReadyWithin passes.
func (o Options) waitReady(ctx context.Context) error {
	within := o.ReadyWithin
	if within <= 0 {
		within = 60 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()

	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Ready, nil)
		if err != nil {
			return fmt.Errorf("building the request for %s: %w", o.Ready, err)
		}

		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("the control plane did not become ready at %s within %s", o.Ready, within)
		case <-time.After(time.Second):
		}
	}
}
