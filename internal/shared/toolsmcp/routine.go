package toolsmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The routines tool (issue #536) is the control plane's, but the script a
// Routine runs is a file of the folder the Bot's pi works in: it is read here
// and its text sent with the call, as show_artifact's files are (artifact.go).
// What runs is that text, kept by the control plane, as `hypomnema routines
// set --script-file` sends it; Hermes's cronjob reads its script from the
// profile's scripts folder (tools/cronjob_tools.py, _script_description).

// RoutineCall reads the script a routines call names, in dir (the folder the
// agent works in), and sends its text with the call on path (PathToolsCall).
// A path that leaves dir, or a file over the limit, is the agent's error.
func (a *Agent) RoutineCall(ctx context.Context, path string, arguments json.RawMessage, dir string) (any, error) {
	var in agenttool.RoutineCall
	if len(arguments) > 0 && json.Unmarshal(arguments, &in) != nil {
		return nil, errors.New("routines takes {action, name, every_minutes, cron, timezone, prompt, script, " +
			"timeout_seconds, summary, quiet}")
	}

	// The text is this server's to give, never the agent's.
	in.ScriptText = nil

	if in.Script != nil && *in.Script != "" {
		text, err := readScript(dir, *in.Script)
		if err != nil {
			return nil, err
		}

		in.ScriptText = &text
	}

	args, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	return a.ToolCall(ctx, path, CallTimeout, agenttool.ToolRoutines, args)
}

// readScript reads a file of dir, never one outside it.
func readScript(dir, name string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("opening the folder you work in: %w", err)
	}

	defer func() { _ = root.Close() }()

	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return "", fmt.Errorf("the script %s cannot be read: give a file in the folder you work in, such as "+
			"routines/report.sh", name)
	}

	defer func() { _ = f.Close() }()

	raw, err := io.ReadAll(io.LimitReader(f, agenttool.MaxRoutineScript+1))
	if err != nil {
		return "", fmt.Errorf("reading the script %s: %w", name, err)
	}

	if len(raw) > agenttool.MaxRoutineScript {
		return "", fmt.Errorf("the script %s is over %d bytes", name, agenttool.MaxRoutineScript)
	}

	return string(raw), nil
}
