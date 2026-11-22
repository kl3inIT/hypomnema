package browser

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// run runs a program to its end with only the environment it is given.
func run(ctx context.Context, c command) (output, error) {
	// A command of the engine's own housekeeping has no deadline of a request's: it gets a call's.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}

	stdout, stderr := capped{limit: c.Limit}, capped{limit: c.Limit}

	//nolint:gosec // The pinned program, with arguments the engine wrote.
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env = c.Env
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// agent-browser leaves a daemon behind; should one keep the output open, the run still ends.
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	out := output{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}

	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		out.Code = exit.ExitCode()

		return out, nil
	}

	if err != nil {
		return output{}, err
	}

	return out, nil
}

// capped keeps the first bytes written to it, up to its limit (a tool's
// largest answer when it is 0) and one more, and drops the rest.
type capped struct {
	buf   bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	limit := c.limit
	if limit == 0 {
		limit = maxResultBytes
	}

	if room := limit + 1 - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}

	return len(p), nil
}
