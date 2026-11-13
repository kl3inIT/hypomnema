package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// The `files serve` command (issue #255): the control plane's user cannot read
// the coordinator's home, so the control plane runs this as the coordinator,
// through a sudoers rule that names it with its root, for each question about
// a Bot's folder, for each change the owner makes in it (issue #297), and for
// each file the Bot sends into a Lark chat (issue #312). As
// Omnigent reads and writes a workspace through a helper run as the agent's
// identity, not as its server.
//
//	hypomnema files serve --root DIR
//
// It reads one BotFilesRequest as JSON on stdin and writes one
// workspacefs.Result to stdout.

// BotFilesRequest is a question about the folder DIR/<bot>.
type BotFilesRequest struct {
	Bot     string              `json:"bot"`
	Request workspacefs.Request `json:"request"`
}

// botName is what a Bot's name may be (orchestration/bots.go).
var botName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func files(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "serve" {
		return fmt.Errorf("%w: files serve --root DIR", errUsage)
	}

	fs := flag.NewFlagSet("files serve", flag.ContinueOnError)
	root := fs.String("root", "", "the folder that holds the Bots' folders")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if *root == "" || !filepath.IsAbs(*root) || fs.NArg() != 0 {
		return fmt.Errorf("%w: files serve --root DIR, an absolute DIR", errUsage)
	}

	var req BotFilesRequest
	if err := json.NewDecoder(io.LimitReader(stdin, 8*workspacefs.MaxWrite)).Decode(&req); err != nil {
		return fmt.Errorf("reading the request: %w", err)
	}

	res := workspacefs.Result{Error: &workspacefs.Error{
		Code: workspacefs.CodeInvalidPath, Message: fmt.Sprintf("%q is not a Bot's name", req.Bot),
	}}
	if botName.MatchString(req.Bot) {
		res = workspacefs.Serve(ctx, filepath.Join(*root, req.Bot), req.Request)
	}

	return json.NewEncoder(stdout).Encode(res)
}
