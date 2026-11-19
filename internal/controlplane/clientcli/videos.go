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
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The videos made of takes (issue #818, row 11), the owner's only: listed,
// read, made, approved, asked for changes, deleted and downloaded.

func videosList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListVideosWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing videos: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing videos", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no videos\n")
		}

		for _, v := range resp.JSON200.Data {
			e.printf("%s\n", videoLine(v))
		}
	})
}

// videoLine is a video in a line: id, its latest revision and state, the approved one, its take, title.
func videoLine(v openapi.Video) string {
	last := v.Revisions[len(v.Revisions)-1]

	approved := ""
	if v.Approved != nil {
		approved = fmt.Sprintf("  approved r%d", *v.Approved)
	}

	return fmt.Sprintf("%s  r%d %s%s  %s  %s  %s", v.Id, last.Rev, revState(last), approved, v.Take,
		v.CreatedAt.Local().Format("2006-01-02 15:04"), v.Title)
}

func revState(r openapi.VideoRevision) string {
	state := string(r.State)

	switch {
	case r.State == openapi.VideoRevisionStateFailed:
		state += " at " + val(r.FailedStage) + " (" + val(r.FailedCause) + ")"
	case r.LengthMs > 0:
		state += " " + clockOf(r.LengthMs)
	}

	return state
}

func videosGet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.GetVideoWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("reading video %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("reading video "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printVideo(resp.JSON200) })
}

func (e *env) printVideo(v *openapi.Video) {
	e.printf("%s\n", videoLine(*v))

	for _, r := range v.Revisions {
		by := ""
		if r.Provider != nil {
			by = "  " + *r.Provider + " " + val(r.UsedVoice)
		}

		captions := "captions"
		if !r.Captions {
			captions = "no captions"
		}

		e.printf("  r%d %s  %s %s%s\n", r.Rev, revState(r), r.Aspect, captions, by)

		if r.Note != nil {
			e.printf("    note: %s\n", *r.Note)
		}

		for _, w := range r.Warnings {
			e.printf("    warning: %s\n", w)
		}
	}

	last := v.Revisions[len(v.Revisions)-1]
	for i, l := range last.Lines {
		e.printf("  %d. %s\n", i+1, l)
	}
}

func videosMake(ctx context.Context, e *env, fs *flag.FlagSet) error {
	body := openapi.VideoMake{Take: fs.Arg(0), Title: optional(fs, "title"), Voice: optional(fs, "voice")}

	if a := str(fs, "aspect"); a != "" {
		aspect := openapi.VideoMakeAspect(a)
		body.Aspect = &aspect
	}

	if str(fs, "no-captions") == "true" {
		off := false
		body.Captions = &off
	}

	resp, err := e.api.MakeVideoWithResponse(ctx, &openapi.MakeVideoParams{IdempotencyKey: key(fs)}, body)
	if err != nil {
		return fmt.Errorf("making a video of %s: %w", body.Take, err)
	}

	if resp.JSON201 == nil {
		return apiError("making a video of "+body.Take, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON201, func() { e.printVideo(resp.JSON201) })
}

// revArg reads a revision's number.
func revArg(s string) (int, error) {
	rev, err := strconv.Atoi(s)
	if err != nil || rev < 1 {
		return 0, fmt.Errorf("%w: a revision is a number from 1", ErrUsage)
	}

	return rev, nil
}

func videosApprove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	rev, err := revArg(fs.Arg(1))
	if err != nil {
		return err
	}

	resp, err := e.api.ApproveVideoRevisionWithResponse(ctx, id, rev)
	if err != nil {
		return fmt.Errorf("approving %s r%d: %w", id, rev, err)
	}

	if resp.JSON200 == nil {
		return apiError(fmt.Sprintf("approving %s r%d", id, rev), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printVideo(resp.JSON200) })
}

func videosRequestChanges(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	rev, err := revArg(fs.Arg(1))
	if err != nil {
		return err
	}

	body := openapi.VideoChanges{Note: optional(fs, "note")}

	if file := str(fs, "lines-file"); file != "" {
		raw, err := e.read(file)
		if err != nil {
			return err
		}

		lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
		body.Lines = &lines
	}

	if body.Note == nil && body.Lines == nil {
		return fmt.Errorf("%w: give --note, --lines-file or both", ErrUsage)
	}

	resp, err := e.api.RequestVideoChangesWithResponse(ctx, id, rev,
		&openapi.RequestVideoChangesParams{IdempotencyKey: key(fs)}, body)
	if err != nil {
		return fmt.Errorf("asking for changes to %s r%d: %w", id, rev, err)
	}

	if resp.JSON200 == nil {
		return apiError(fmt.Sprintf("asking for changes to %s r%d", id, rev), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printVideo(resp.JSON200) })
}

func videosDelete(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteVideoWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting video %s: %w", id, err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("deleting video "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]any{"deleted": id}, func() { e.printf("deleted video %s\n", id) })
}

func videosDownload(ctx context.Context, e *env, flags *flag.FlagSet) error {
	id := flags.Arg(0)

	rev, err := revArg(flags.Arg(1))
	if err != nil {
		return err
	}

	name := fmt.Sprintf("%s-r%d.mp4", id, rev)
	if out := str(flags, "out"); out != "" {
		name = out
	}

	resp, err := e.api.ReadVideoFile(ctx, id, rev, &openapi.ReadVideoFileParams{})
	if err != nil {
		return fmt.Errorf("downloading %s r%d: %w", id, rev, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

		return fmt.Errorf("downloading %s r%d: HTTP %d: %s", id, rev, resp.StatusCode, said)
	}

	// Written as it comes, to a new file that never replaces one, as a take's zip is.
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
		e.printf("saved %s r%d as %s (%s)\n", id, rev, name, size(n))
	})
}

func audioVoice(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, voice := fs.Arg(0), fs.Arg(1)

	resp, err := e.api.SetAudioVoiceWithResponse(ctx, openapi.AudioProvider(provider), openapi.AudioVoice{Voice: voice})
	if err != nil {
		return fmt.Errorf("setting %s's voice: %w", provider, err)
	}

	if resp.JSON200 == nil {
		return apiError("setting "+provider+"'s voice", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if voice == "" {
			e.printf("audio provider %s speaks videos with its own default voice\n", provider)

			return
		}

		e.printf("audio provider %s speaks videos with %s\n", provider, voice)
	})
}
