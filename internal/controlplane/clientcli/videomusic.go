package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The music library a video's music is chosen from (issue #910): listed, a track uploaded and deleted, the owner's
// only.

func videosMusicList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListVideoMusicWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing the music: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the music", resp.StatusCode(), resp.JSONDefault)
	}

	lib := resp.JSON200

	return e.print(fs, lib, func() {
		if len(lib.Tracks) == 0 {
			e.printf("no music\n")
		}

		for _, t := range lib.Tracks {
			e.printf("%s\n", musicTrackLine(t))
		}

		e.printf("%s of %s used\n", size(lib.UsedBytes), size(lib.QuotaBytes))
	})
}

// musicTrackLine is a track in a line: id, length, size, name, and whether its file is missing.
func musicTrackLine(t openapi.VideoMusicTrack) string {
	line := fmt.Sprintf("%s  %s  %s  %s", t.Id, clockOf(t.LengthMs), size(t.Bytes), t.Name)
	if t.Missing != nil && *t.Missing {
		line += "  (missing)"
	}

	return line
}

// videosMusicAdd uploads a file as it is, with its length, to the route outside the contract
// (POST /api/v1/videos/music?name=…).
func videosMusicAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	path := fs.Arg(0)

	name := str(fs, "name")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	f, err := os.Open(path) //nolint:gosec // The owner's own file, named on the command line.
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // Read only.

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	client, ok := e.api.ClientInterface.(*openapi.Client)
	if !ok {
		return errors.New("the API client cannot upload")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(client.Server, "/")+"/videos/music?name="+url.QueryEscape(name), f)
	if err != nil {
		return err
	}

	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/octet-stream")

	for _, edit := range client.RequestEditors {
		if err := edit(ctx, req); err != nil {
			return err
		}
	}

	resp, err := client.Client.Do(req)
	if err != nil {
		return fmt.Errorf("uploading %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // Read only.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("uploading %s: %w", path, err)
	}

	if resp.StatusCode != http.StatusCreated {
		var said openapi.Error
		if json.Unmarshal(raw, &said) != nil || said.Code == "" {
			return fmt.Errorf("uploading %s: HTTP %d", path, resp.StatusCode)
		}

		return apiError("uploading "+path, resp.StatusCode, &said)
	}

	var t openapi.VideoMusicTrack
	if err := json.Unmarshal(raw, &t); err != nil {
		return fmt.Errorf("reading the answer to uploading %s: %w", path, err)
	}

	return e.print(fs, t, func() { e.printf("added %s\n", musicTrackLine(t)) })
}

func videosMusicRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteVideoMusicWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting music track %s: %w", id, err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("deleting music track "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]any{"deleted": id}, func() { e.printf("deleted music track %s\n", id) })
}
