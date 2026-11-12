package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// The owner reads a Task's worktree from the web (issue #255). The control
// plane asks on the socket, and the node answers with an HTTP request of its
// own, since a file may be larger than a socket message: as Omnigent's runner
// answers its server over its own connection.

// answerFiles answers a files_request. It does not hold up the socket: the
// answer is read and sent on its own.
func (d *Daemon) answerFiles(payload json.RawMessage) error {
	var req nodeproto.FilesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("decoding a files request: %w", err)
	}

	if d.base == nil {
		return nil
	}

	go func() {
		ctx, cancel := context.WithTimeout(d.base, nodeproto.FilesAnswerWithin)
		defer cancel()

		res := d.files(ctx, req)
		if err := d.postFiles(ctx, req.RequestID, res); err != nil && ctx.Err() == nil {
			d.logger.WarnContext(ctx, "files answer not sent",
				slog.String("event", "node.files_failed"), slog.String("error", err.Error()))
		}
	}()

	return nil
}

// files reads a worktree: only one under this node's worktree root.
func (d *Daemon) files(ctx context.Context, req nodeproto.FilesRequest) workspacefs.Result {
	refuse := func(code workspacefs.Code, msg string) workspacefs.Result {
		return workspacefs.Result{Error: &workspacefs.Error{Code: code, Message: msg}}
	}

	if d.cfg.WorktreeRoot == "" {
		return refuse(workspacefs.CodeInvalidPath, "this node keeps no worktrees")
	}

	root, err := filepath.EvalSymlinks(d.cfg.WorktreeRoot)
	if err != nil {
		return refuse(workspacefs.CodeFailed, "reading the worktree root: "+err.Error())
	}

	place, err := filepath.EvalSymlinks(req.Worktree)
	if errors.Is(err, fs.ErrNotExist) {
		return refuse(workspacefs.CodeGone, "the worktree "+req.Worktree+" is not there any more")
	}

	if err != nil || !filepath.IsAbs(req.Worktree) || !workspacefs.Inside(root, place) || place == root {
		return refuse(workspacefs.CodeInvalidPath, req.Worktree+" is not a worktree of this node")
	}

	return workspacefs.Serve(ctx, place, req.Request)
}

func (d *Daemon) postFiles(ctx context.Context, requestID string, res workspacefs.Result) error {
	body, err := json.Marshal(res)
	if err != nil {
		return err
	}

	target := strings.TrimSuffix(d.cfg.URL, "/") + nodeproto.PathFiles + "?request=" + url.QueryEscape(requestID)

	r, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}

	r.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	r.Header.Set("Content-Type", "application/json")

	resp, err := d.http.Do(r)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("the control plane answered %s", resp.Status)
	}

	return nil
}
