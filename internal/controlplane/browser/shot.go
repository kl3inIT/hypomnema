package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// A screenshot asked with save_to (issue #1059) is written on the agent's
// machine by its own tools server (toolsmcp's ShotCall), from the picture
// this engine sends. agent-browser gives a model a screenshot of at most
// 10 MiB as an image and a larger one as the path of the file it saved
// (cli/src/mcp.rs:187, 4515-4545, image_content_from_response 4698-4711, at
// 0.42.0), which is on this machine; and the engine keeps 8 MiB of another
// answer. So the picture is read from that file, as agent-browser itself
// reads it into its answer, and sent whole up to agenttool.MaxSavedShotBytes,
// the bound of generate_image's answer (the coordinator's choice for the owner
// in review round 1 of PR #1062, 2026-11-22).

// shotLineBytes bounds agent-browser's answer to a screenshot to save: its own
// image of up to 10 MiB in base64, which is not sent, and the rest.
const shotLineBytes = 16 << 20

// shotTypes are the pictures agent-browser saves, by extension, as its
// image_mime_type reads them (cli/src/mcp.rs).
var shotTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg"}

// SendsWhole says whether a call is a screenshot asked with save_to, whose
// answer is the picture whole, up to agenttool.MaxSavedShotBytes.
func SendsWhole(tool string, arguments json.RawMessage) bool {
	var given map[string]json.RawMessage

	return tool == agenttool.ToolBrowserScreenshot && json.Unmarshal(arguments, &given) == nil &&
		given[devicetool.SaveTo] != nil
}

// savedShot takes a screenshot and answers with the file agent-browser saved,
// whole, as the image it is; agent-browser's own answer when it failed.
func (e *Engine) savedShot(ctx context.Context, s *session, cdp string, arguments json.RawMessage) (
	json.RawMessage, error,
) {
	answer, err := e.answer(ctx, s, cdp, agenttool.ToolBrowserScreenshot, arguments, shotLineBytes)
	if err != nil || answer.IsError {
		if err != nil {
			return nil, err
		}

		return answer.result()
	}

	data, mime, why := e.readShot(answer.Structured.Response.Data.Path)
	if why != "" {
		return failedShot(why)
	}

	return json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "image", "data": data, "mimeType": mime}}, "isError": false,
	})
}

// readShot reads a saved screenshot in base64, with its type, or says why it cannot be sent. Only a file in the
// folder agent-browser saves a screenshot in when it is given no path (native/screenshot.rs:658-666, under the
// HOME the engine gives it), which an agent cannot name (withheldOf), is read.
func (e *Engine) readShot(path string) (string, string, string) {
	mime := shotTypes[strings.ToLower(filepath.Ext(path))]

	within, err := filepath.Rel(filepath.Join(e.home(), ".agent-browser", "tmp", "screenshots"), path)
	if path == "" || err != nil || !filepath.IsLocal(within) || mime == "" {
		return "", "", "The browser did not say where it saved the screenshot, so it could not be sent to be saved."
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", "", "The screenshot the browser saved could not be read: " + err.Error()
	}

	// What surrounds the picture in the answer is well under a KiB.
	if base64.StdEncoding.EncodedLen(int(info.Size()))+1<<10 > agenttool.MaxSavedShotBytes {
		return "", "", fmt.Sprintf("The screenshot is %d MB, more than can be sent to be saved (%d MB once encoded): "+
			"take a part of the page with selector, or format jpeg.", info.Size()>>20, agenttool.MaxSavedShotBytes>>20)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // agent-browser's own file, in its folder under the engine's.
	if err != nil {
		return "", "", "The screenshot the browser saved could not be read: " + err.Error()
	}

	return base64.StdEncoding.EncodeToString(raw), mime, ""
}

func failedShot(why string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": why}}, "isError": true})
}
