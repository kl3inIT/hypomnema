package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// botFiles reads a Bot's folder (issue #255) by running `hypomnema files
// serve` as the coordinator, once for each question: the control plane's user
// cannot read the coordinator's home.
type botFiles struct {
	command []string
	root    string
}

// newBotFiles is how the client API reads the Bots' folders, or nil when
// HYPOMNEMA_BOT_FILES_COMMAND is not set.
func newBotFiles(command []string) clientapi.BotFiles {
	if len(command) == 0 {
		return nil
	}

	root := ""
	if i := slices.Index(command, "--root"); i >= 0 && i+1 < len(command) {
		root = command[i+1]
	}

	return &botFiles{command: command, root: root}
}

func (b *botFiles) Root(bot string) string {
	if b.root == "" {
		return bot
	}

	return path.Join(b.root, bot)
}

func (b *botFiles) Ask(ctx context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, nodeproto.FilesAnswerWithin)
	defer cancel()

	question, err := json.Marshal(struct {
		Bot     string              `json:"bot"`
		Request workspacefs.Request `json:"request"`
	}{bot, req})
	if err != nil {
		return workspacefs.Result{}, err
	}

	cmd := exec.CommandContext(ctx, b.command[0], b.command[1:]...) //nolint:gosec // the owner's own command
	cmd.Stdin = bytes.NewReader(question)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limited{w: &stdout, left: nodeproto.MaxFilesAnswerBytes}
	cmd.Stderr = &limited{w: &stderr, left: 4 << 10}

	if err := cmd.Run(); err != nil {
		return workspacefs.Result{}, fmt.Errorf("reading bot %s's folder: %w: %s", bot, err,
			strings.TrimSpace(stderr.String()))
	}

	var res workspacefs.Result
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return workspacefs.Result{}, fmt.Errorf("reading bot %s's folder: the answer: %w", bot, err)
	}

	return res, nil
}

// limited keeps at most `left` bytes and drops the rest.
type limited struct {
	w    io.Writer
	left int
}

func (l *limited) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > l.left {
		p = p[:l.left]
	}

	l.left -= len(p)
	if _, err := l.w.Write(p); err != nil {
		return 0, err
	}

	return n, nil
}
