package toolsmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// show_artifact (issue #536) is the control plane's, as memory is, but the
// file or folder it shows is on the agent's machine: it is read here, by the
// rules `hypomnema artifact show` reads by (artifactpack.Read), and sent whole
// with the call, as generate_image sends the images it edits (image.go).
// OpenMausBot's show_html reads its path on the server, which shares the
// bot's disk (server/pages/bundle.ts); a worker's node does not share the
// control plane's. The control plane checks the files again.

// ArtifactCall reads the file or folder show_artifact names, in dir (the
// folder the agent works in), sends its files with the call on path
// (PathToolsCall), and adds to the result what a folder left out, as
// OpenMausBot's show_html adds its leftOut note. What the agent can fix is
// its error.
func (a *Agent) ArtifactCall(ctx context.Context, path string, arguments json.RawMessage, dir string) (any, error) {
	var in agenttool.ArtifactCall
	if len(arguments) > 0 && json.Unmarshal(arguments, &in) != nil {
		return nil, errors.New("show_artifact takes {title, html, path, id}")
	}

	// The files are this server's to give, never the agent's.
	in.Files = nil

	var skipped []artifactpack.Skipped

	if in.Path != "" && in.HTML == "" {
		files, left, err := artifactpack.Read(dir, in.Path)
		if err != nil {
			return nil, err
		}

		// Checked here too, so that what is over a limit is refused before it is sent.
		if _, err := artifactpack.Check(files); err != nil {
			return nil, err
		}

		for _, f := range files {
			in.Files = append(in.Files, agenttool.ArtifactFile{Path: f.Path, Data: f.Data})
		}

		skipped = left
	}

	args, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	raw, err := a.ToolCall(ctx, path, CallTimeout, agenttool.ToolShowArtifact, args)
	if err != nil {
		return nil, err
	}

	note := artifactpack.SkippedNote(skipped)
	if note == "" {
		return raw, nil
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}

	// An answer that cannot be read, or the tool's error, is the agent's as the control plane gave it.
	if json.Unmarshal(raw, &result) == nil && !result.IsError {
		lines := make([]string, 0, len(result.Content)+1)
		for _, c := range result.Content {
			lines = append(lines, c.Text)
		}

		return Text(strings.Join(append(lines, note), "\n"), false), nil
	}

	return raw, nil
}
