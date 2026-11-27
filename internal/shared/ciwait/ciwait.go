// Package ciwait is `hypomnema ci wait <pr>` (issue #1262), for the coordinator's CLI and a worker's alike: it waits
// for the required checks of a pull request's head to end by asking the control plane again and again, each ask
// held there until the GitHub App's events change the pull request's CI or a while passes. The command calls no
// GitHub itself; the control plane reads the checks when an event says CI ended, and once every five minutes when
// none comes.
package ciwait

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// State is the CI of a pull request as the control plane answers it.
type State struct {
	PullRequest string `json:"pull_request"`
	Head        string `json:"head,omitempty"`
	// State is pending, passed, failed, merged or closed (orchestration.CIState).
	State  string   `json:"state"`
	Failed []string `json:"failed,omitempty"`
}

// Done says the wait ends on this state.
func (s State) Done() bool { return s.State != "" && s.State != "pending" }

// Exit codes, as gh pr checks has them (pkg/cmd/pr/checks: 1 for a failure, 8 for checks still pending).
const (
	ExitPassed  = 0
	ExitFailed  = 1
	ExitPending = 8
)

// Code is the command's exit code for a state: a merge passed, a close without one failed.
func (s State) Code() int {
	switch s.State {
	case "passed", "merged":
		return ExitPassed
	case "failed", "closed":
		return ExitFailed
	default:
		return ExitPending
	}
}

// Line is the one line the command prints of a state.
func (s State) Line() string {
	head := s.Head
	if len(head) > 12 {
		head = head[:12]
	}

	line := fmt.Sprintf("%s\t%s", s.PullRequest, s.State)
	if head != "" {
		line += "\tat " + head
	}

	if len(s.Failed) > 0 {
		line += "\tfailed: " + strings.Join(s.Failed, ", ")
	}

	return line
}

// Hold is how long one ask is held by the control plane: under the minute a reverse proxy keeps a quiet request
// open by default (nginx's proxy_read_timeout, which the owner's HTTPS front is).
const Hold = 50 * time.Second

// Ask asks the control plane for the pull request's CI, held at most hold.
type Ask func(ctx context.Context, hold time.Duration) (State, error)

// ErrTimeout says the checks were still pending when the wait gave up.
var ErrTimeout = errors.New("the checks are still pending")

// Stop marks an ask's error as one asking again does not change (no such route, a repository the App does not
// cover): the wait ends with it at once.
func Stop(err error) error { return &stopError{err: err} }

type stopError struct{ err error }

func (e *stopError) Error() string { return e.err.Error() }
func (e *stopError) Unwrap() error { return e.err }

// retries is how many asks in a row may fail, a pause apart, before the wait does.
const (
	retries = 5
	pause   = 15 * time.Second
)

// Wait asks until the CI is done or timeout passes (none when zero), printing each change of state to out. An ask
// that fails is tried again after a pause, a few times in a row, since a control plane that restarts answers again
// within seconds.
func Wait(ctx context.Context, ask Ask, timeout time.Duration, out io.Writer) (State, error) {
	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var (
		last   State
		failed int
		// The first ask is answered at once, so the state is printed before the first hold.
		hold time.Duration
	)

	for {
		st, err := ask(ctx, hold)
		if err != nil {
			var stop *stopError
			if errors.As(err, &stop) {
				return last, stop.err
			}

			if ctx.Err() != nil {
				return last, timedOut(ctx, last)
			}

			failed++
			if failed >= retries {
				return last, err
			}

			select {
			case <-ctx.Done():
				return last, timedOut(ctx, last)
			case <-time.After(pause):
			}

			continue
		}

		failed, hold = 0, Hold

		if st.State != last.State || st.Head != last.Head {
			_, _ = fmt.Fprintln(out, st.Line())
		}

		last = st
		if st.Done() {
			return st, nil
		}

		if ctx.Err() != nil {
			return last, timedOut(ctx, last)
		}
	}
}

func timedOut(ctx context.Context, last State) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s", ErrTimeout, last.Line())
	}

	return ctx.Err()
}

// Interval is how often the gh fallback reads the checks, in seconds: once a minute, since GitHub's REST limit is
// shared with every other call of the account (issue #1262, owner, 2026-11-27).
const Interval = "60"

// GH waits for the required checks with `gh pr checks <pr> --required --watch --interval 60`, for a repository the
// GitHub App does not cover or a control plane without the route. ctx bounds it: gh is stopped when it ends, and
// the wait is then still pending (review round 1).
func GH(ctx context.Context, pr string, out, errOut io.Writer) error {
	//nolint:gosec // G204: gh, with the pull request the caller named as one argument, never through a shell
	cmd := exec.CommandContext(ctx, "gh", "pr", "checks", pr, "--required", "--watch", "--interval", Interval)
	cmd.Stdout, cmd.Stderr = out, errOut
	// A child gh started that holds its output open does not keep the wait past its end.
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		_, _ = fmt.Fprintf(errOut, "hypomnema: %v: %s\n", ErrTimeout, pr)

		return &agenttool.ExitError{Code: ExitPending}
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return &agenttool.ExitError{Code: exit.ExitCode()}
	}

	return err
}

// Run is `hypomnema ci wait <pr> [--timeout]`: it asks until the CI is done, or waits with gh (GH) when an ask's error
// says to (fallback), all within timeout (none when zero). It ends nil when the checks passed or the pull request
// merged, and with gh's exit codes otherwise (agenttool.ExitError): 1 failed or closed, 8 still pending at the
// timeout.
func Run(ctx context.Context, pr string, ask Ask, timeout time.Duration, fallback func(error) bool,
	out, errOut io.Writer,
) error {
	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	st, err := Wait(ctx, ask, 0, out)

	switch {
	case err != nil && fallback(err):
		_, _ = fmt.Fprintf(errOut, "hypomnema: %v; waiting with gh instead\n", err)

		return GH(ctx, pr, out, errOut)
	case errors.Is(err, ErrTimeout):
		_, _ = fmt.Fprintf(errOut, "hypomnema: %v\n", err)

		return &agenttool.ExitError{Code: ExitPending}
	case err != nil:
		return err
	}

	if code := st.Code(); code != ExitPassed {
		return &agenttool.ExitError{Code: code}
	}

	return nil
}
