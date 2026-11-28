package clientcli

import (
	"context"
	"flag"
	"fmt"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The notes of a memory from the terminal (issue #589): a Bot's or a profile's directory /memories, whose
// MEMORY.md is the memory `memory show` prints. The owner reads, writes and removes them; an agent writes its own
// with the memory's tool.

// notesFlags are memoryFlags: --owner is refused, the memory about the owner having no notes.
func notesFlags(fs *flag.FlagSet) { memoryFlags(fs) }

// notesOf is --profile, --bot or the Bot the calling pi is, as the API's query names it.
func notesOf(ctx context.Context, e *env, fs *flag.FlagSet) (bot, profile *string, err error) {
	if boolean(fs, "owner") {
		return nil, nil, fmt.Errorf("%w: the memory about the owner is /memories/USER.md, and has no notes", ErrUsage)
	}

	target, err := memoryTarget(ctx, e, fs)
	if err != nil {
		return nil, nil, err
	}

	if target.profile != "" {
		return nil, &target.profile, nil
	}

	if target.bot == "" {
		return nil, nil, fmt.Errorf("%w: name the memory with --bot or --profile", ErrUsage)
	}

	id, err := botIDOf(ctx, e, target.bot)
	if err != nil {
		return nil, nil, err
	}

	return &id, nil, nil
}

func notesList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	bot, profile, err := notesOf(ctx, e, fs)
	if err != nil {
		return err
	}

	resp, err := e.api.ListMemoryNotesWithResponse(ctx, &openapi.ListMemoryNotesParams{Bot: bot, Profile: profile})
	if err != nil {
		return fmt.Errorf("listing the notes: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the notes", resp.StatusCode(), resp.JSONDefault)
	}

	l := resp.JSON200

	return e.print(fs, l, func() {
		e.printf("%d of %d notes\n", len(l.Data), l.MaxNotes)

		for _, n := range l.Data {
			e.printf("%s\t%s\n", agenttool.MemoryNote(n.Path, n.Bytes), n.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
	})
}

func notesCat(ctx context.Context, e *env, fs *flag.FlagSet) error {
	bot, profile, err := notesOf(ctx, e, fs)
	if err != nil {
		return err
	}

	resp, err := e.api.GetMemoryNoteWithResponse(ctx, &openapi.GetMemoryNoteParams{
		Bot: bot, Profile: profile, Path: fs.Arg(0),
	})
	if err != nil {
		return fmt.Errorf("reading %s: %w", fs.Arg(0), err)
	}

	if resp.JSON200 == nil {
		return apiError("reading "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s", resp.JSON200.Text) })
}

func notesPut(ctx context.Context, e *env, fs *flag.FlagSet) error {
	bot, profile, err := notesOf(ctx, e, fs)
	if err != nil {
		return err
	}

	text, err := e.read(fs.Arg(1))
	if err != nil {
		return err
	}

	resp, err := e.api.PutMemoryNoteWithResponse(ctx, &openapi.PutMemoryNoteParams{
		Bot: bot, Profile: profile, Path: fs.Arg(0),
	}, openapi.MemoryNoteText{Text: text})
	if err != nil {
		return fmt.Errorf("writing %s: %w", fs.Arg(0), err)
	}

	if resp.JSON200 == nil {
		return apiError("writing "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	n := resp.JSON200

	return e.print(fs, n, func() { e.printf("%s is kept\n", agenttool.MemoryNote(n.Path, n.Bytes)) })
}

func notesRm(ctx context.Context, e *env, fs *flag.FlagSet) error {
	bot, profile, err := notesOf(ctx, e, fs)
	if err != nil {
		return err
	}

	resp, err := e.api.DeleteMemoryNoteWithResponse(ctx, &openapi.DeleteMemoryNoteParams{
		Bot: bot, Profile: profile, Path: fs.Arg(0),
	})
	if err != nil {
		return fmt.Errorf("removing %s: %w", fs.Arg(0), err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("removing "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]bool{"removed": true}, func() { e.printf("%s is removed\n", fs.Arg(0)) })
}
