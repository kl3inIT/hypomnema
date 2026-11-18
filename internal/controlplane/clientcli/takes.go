package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The takes agents film of the phone (issue #817): listed, read, downloaded
// as a zip of their segments and take.json, and deleted, the owner's only.

func takesList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListTakesWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing takes: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing takes", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no takes\n")
		}

		for _, t := range resp.JSON200.Data {
			e.printf("%s\n", takeLine(t))
		}
	})
}

// takeLine is a take in a line: id, state, length, steps, when, by whom, title.
func takeLine(t openapi.Take) string {
	state := t.State
	if t.Reason != nil && *t.Reason != "" {
		state += " (" + *t.Reason + ")"
	}

	by := t.Agent
	if t.AgentName != nil && *t.AgentName != "" {
		by = *t.AgentName
	}

	return fmt.Sprintf("%s  %s  %s  %d steps  %s  %s  %s", t.Id, state, clockOf(t.DurationMs), len(t.Steps),
		t.StartedAt.Local().Format("2006-01-02 15:04"), by, t.Title)
}

// clockOf is a take's time as minutes and seconds.
func clockOf(ms int64) string {
	s := max(ms, 0) / 1000

	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func takesGet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.GetTakeWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("reading take %s: %w", id, err)
	}

	t := resp.JSON200
	if t == nil {
		return apiError("reading take "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, t, func() {
		e.printf("%s\n", takeLine(*t))

		for _, s := range t.Segments {
			e.printf("  segment %s  at %s  %s  %dx%d  %s\n", s.File, clockOf(s.AtMs), clockOf(s.LengthMs), s.Width,
				s.Height, size(s.Bytes))
		}

		for _, s := range t.Steps {
			chapter := ""
			if s.Chapter != nil && *s.Chapter != "" {
				chapter = "[" + *s.Chapter + "] "
			}

			e.printf("  %s  %s%s\n", clockOf(s.AtMs), chapter, s.Text)
		}

		for _, p := range t.Pauses {
			e.printf("  %s  paused for the owner\n", clockOf(p.AtMs))
		}
	})
}

func takesDownload(ctx context.Context, e *env, flags *flag.FlagSet) error {
	id := flags.Arg(0)

	name := id + ".zip"
	if out := str(flags, "out"); out != "" {
		name = out
	}

	resp, err := e.api.GetTakeArchive(ctx, id)
	if err != nil {
		return fmt.Errorf("downloading take %s: %w", id, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

		return fmt.Errorf("downloading take %s: HTTP %d: %s", id, resp.StatusCode, said)
	}

	// A take is large: it is written as it comes, to a new file that never replaces one.
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a name of this directory
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s is already there and is not replaced; give another name with --out", name)
	}

	if err != nil {
		return fmt.Errorf("saving %s: %w", name, err)
	}

	n, err := io.Copy(f, resp.Body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("saving %s: %w", name, err)
	}

	return e.print(flags, map[string]any{"file": name, "size": n}, func() {
		e.printf("saved take %s as %s (%s)\n", id, name, size(n))
	})
}

func takesDelete(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteTakeWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting take %s: %w", id, err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("deleting take "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]any{"deleted": id}, func() { e.printf("deleted take %s\n", id) })
}
