package clientcli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Artifacts from the terminal (issue #402). A Bot shows a page, a PDF, an image or any file to the owner in its
// chat with `artifact show`, as OpenMausBot's bots call show_html (server/drivers/agents-app-tools.ts: html, path,
// app_id); the file or folder is read in the Bot's directory, which is pi's working directory, and the id printed
// is what replaces it later. Every token lists and reads artifacts.

func artifactShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	htmlFile, target, id := str(fs, "html-file"), str(fs, "path"), str(fs, "id")
	title := str(fs, "title")

	if (htmlFile == "") == (target == "") {
		return fmt.Errorf("%w: give --html-file <file> or --path <file or folder>", ErrUsage)
	}

	if title == "" && id == "" {
		return fmt.Errorf("%w: a new artifact needs --title", ErrUsage)
	}

	var (
		files   []artifactpack.File
		skipped []artifactpack.Skipped
	)

	if htmlFile != "" {
		page, err := e.read(htmlFile)
		if err != nil {
			return err
		}

		files = []artifactpack.File{{Path: artifactpack.Entry, Data: []byte(page)}}
	} else {
		dir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("reading the working directory: %w", err)
		}

		files, skipped, err = artifactpack.Read(dir, target)
		if err != nil {
			return err
		}
	}

	// Checked here too, so that a page over a limit is refused before it is sent.
	if _, err := artifactpack.Check(files); err != nil {
		return err
	}

	body, err := artifactpack.Tar(files)
	if err != nil {
		return err
	}

	params := &openapi.ShowArtifactParams{Title: optional(fs, "title"), Id: optional(fs, "id")}

	resp, err := e.api.ShowArtifactWithBodyWithResponse(ctx, params, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("showing the artifact: %w", err)
	}

	a, status := resp.JSON201, resp.StatusCode()
	if a == nil {
		a = resp.JSON200
	}

	if a == nil {
		return apiError("showing the artifact", status, resp.JSONDefault)
	}

	return e.print(fs, a, func() {
		if status == http.StatusCreated {
			e.printf("%s revision %d: %s (%s, %s)\n", a.Id, a.Revision, a.Title, a.Type, size(a.Size))
		} else {
			e.printf("%s revision %d: the files and title are unchanged; nothing shown\n", a.Id, a.Revision)
		}

		if note := artifactpack.SkippedNote(skipped); note != "" {
			e.printf("%s\n", note)
		}
	})
}

func artifactList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListArtifactsWithResponse(ctx, &openapi.ListArtifactsParams{
		ChatId: optional(fs, "chat"), Limit: limit(fs),
	})
	if err != nil {
		return fmt.Errorf("listing artifacts: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing artifacts", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no artifacts\n")
		}

		for _, a := range resp.JSON200.Data {
			e.printf("%s  r%d  %-5s  %s  %s  %s\n", a.Id, a.Revision, a.Type, a.Bot, a.UpdatedAt.Local().
				Format("2006-01-02 15:04"), a.Title)
		}
	})
}

func limit(fs *flag.FlagSet) *int {
	if n := number(fs, "limit"); n > 0 {
		return &n
	}

	return nil
}

func artifactInfo(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.GetArtifactWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("reading artifact %s: %w", id, err)
	}

	a := resp.JSON200
	if a == nil {
		return apiError("reading artifact "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, a, func() {
		e.printf("%s: %s\n", a.Id, a.Title)
		e.printf("  by %s in chat %s, %s, revision %d\n", a.Bot, a.ChatId, a.Type, a.Revision)

		for _, r := range val(a.Revisions) {
			e.printf("  revision %d  %s  %s  %s\n", r.Revision, r.CreatedAt.Local().Format("2006-01-02 15:04"),
				size(r.Size), r.Title)

			for _, f := range r.Files {
				e.printf("    %s  %s  %s\n", f.Path, size(f.Size), f.MediaType)
			}
		}
	})
}

// artifactDownload saves a revision: its one file as it is, a folder as a zip.
func artifactDownload(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	info, err := e.api.GetArtifactWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("reading artifact %s: %w", id, err)
	}

	a := info.JSON200
	if a == nil {
		return apiError("reading artifact "+id, info.StatusCode(), info.JSONDefault)
	}

	rev := number(fs, "rev")
	if rev == 0 {
		rev = a.Revision
	}

	var files int

	for _, r := range val(a.Revisions) {
		if r.Revision == rev {
			files = len(r.Files)
		}
	}

	var resp *http.Response

	// The name comes from the server: it only names the file in this directory, after the artifact and revision.
	name := downloadName(id, rev, a.Entry)
	if files == 1 {
		resp, err = e.api.ReadArtifactFile(ctx, id, rev, &openapi.ReadArtifactFileParams{})
	} else {
		resp, err = e.api.GetArtifactArchive(ctx, id, rev)
		name = downloadName(id, rev, "") + ".zip"
	}

	if err != nil {
		return fmt.Errorf("downloading artifact %s: %w", id, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading artifact %s: HTTP %d", id, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("downloading artifact %s: %w", id, err)
	}

	if out := str(fs, "out"); out != "" {
		name = out
	}

	if err := saveNew(name, data); err != nil {
		return err
	}

	return e.print(fs, map[string]any{"file": name, "size": len(data)}, func() {
		e.printf("saved revision %d of %s as %s (%s)\n", rev, id, name, size(int64(len(data))))
	})
}

// downloadName is the file a revision is saved as by default: <id>-r<rev>-<name>, its name being the last part of
// what the server named it when that is a plain file name here, and nothing when it is not.
func downloadName(id string, rev int, entry string) string {
	name := id + "-r" + strconv.Itoa(rev)
	if base := safeBase(entry); base != "" {
		name += "-" + base
	}

	return name
}

// windowsReserved are the device names Windows refuses as a file name, with or without an extension.
var windowsReserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)

// safeBase is the last part of a name the server gave, or "" when it is no plain file name: empty, "." or "..",
// with a separator, a drive's colon or a control character, or a device name of Windows.
func safeBase(name string) string {
	base := name[strings.LastIndexAny(name, `/\`)+1:]

	if base == "" || base == "." || base == ".." || strings.ContainsAny(base, ":/\\") ||
		windowsReserved.MatchString(base) {
		return ""
	}

	for _, r := range base {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}

	return base
}

// saveNew writes a new file and never replaces one that is there.
func saveNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a name of this directory
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s is already there and is not replaced; give another name with --out", name)
	}

	if err != nil {
		return fmt.Errorf("saving %s: %w", name, err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()

		return fmt.Errorf("saving %s: %w", name, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("saving %s: %w", name, err)
	}

	return nil
}

// size is a number of bytes as people read it.
func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
