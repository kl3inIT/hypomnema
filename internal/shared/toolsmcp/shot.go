package toolsmcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// A screenshot saved to a file (issue #1059). device_screen and the browser's
// screenshot are run by the control plane, on another machine, and answer
// with the picture; the folder the agent works in is this server's machine's.
// With save_to the picture is written here, and the answer is its path
// without the picture, as mobile-mcp's mobile_save_screenshot answers
// "Screenshot saved to: <path>" (src/server.ts:1002-1031). As there, the name
// ends in .png, .jpg or .jpeg (server.ts:30) and says the file's format, and a
// path out of the allowed folder is refused before the screen is taken
// (src/utils.ts:144-163, validateOutputPath); the one folder allowed is the
// one the agent works in, whose files a Bot's artifacts and chat take. A link
// in it cannot carry the write out of it (os.Root, as utils.ts:95-142 follows
// every link to refuse one), and the file is written beside its name and
// renamed over it (utils.ts:165-185). Folders on the way are made, as
// generate_image makes them (image.go, saveImage).

// shotQuality is the JPEG quality a PNG is written at when a .jpg is asked,
// mobile-mcp's for its screenshot (src/server.ts, mobile_take_screenshot: quality 75).
const shotQuality = 75

// shotFormats are the picture formats a name may ask for, by its extension.
var shotFormats = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg"}

// ShotCall runs a screenshot tool (device_screen, agent_browser_screenshot) on path within the time given. Without
// save_to it is ToolCall. With save_to, taken out of the arguments sent, the picture is written there, in dir (the
// folder the agent works in), and the answer is its path. What the agent can fix is its error.
func (a *Agent) ShotCall(ctx context.Context, path string, within time.Duration, tool string,
	arguments json.RawMessage, dir string,
) (any, error) {
	var given map[string]json.RawMessage
	if len(arguments) == 0 || json.Unmarshal(arguments, &given) != nil || given[devicetool.SaveTo] == nil {
		// Arguments that are not an object are the control plane's to refuse.
		return a.ToolCall(ctx, path, within, tool, arguments)
	}

	var asked string
	if json.Unmarshal(given[devicetool.SaveTo], &asked) != nil || strings.TrimSpace(asked) == "" {
		return nil, errors.New("save_to is a path, such as shots/home.png")
	}

	rel, format, err := shotPath(dir, asked)
	if err != nil {
		return nil, err
	}

	// The browser's control plane is told of save_to, and sends the picture whole from the file the browser saved
	// rather than the image a model is given, which is bounded (internal/controlplane/browser, savedShot). The
	// phone's answer is its whole picture already, and the phone takes no save_to.
	if tool != agenttool.ToolBrowserScreenshot {
		delete(given, devicetool.SaveTo)
	}

	sent, err := json.Marshal(given)
	if err != nil {
		return nil, err
	}

	raw, err := a.toolCall(ctx, path, within, agenttool.MaxSavedShotBytes, tool, sent)
	if err != nil {
		return nil, err
	}

	var result struct {
		Content []shotContent `json:"content"`
		IsError bool          `json:"isError"`
	}

	// An answer that cannot be read, or the tool's error, is the agent's as the control plane gave it.
	if json.Unmarshal(raw, &result) == nil && !result.IsError {
		return saveShot(result.Content, dir, rel, format, asked)
	}

	return raw, nil
}

// shotContent is one item of a screenshot tool's answer.
type shotContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Data string `json:"data"`
}

// saveShot writes the answer's picture at rel in dir and says where; an
// answer without one is the agent's error, with what the tool said.
func saveShot(content []shotContent, dir, rel, format, asked string) (any, error) {
	var said []string

	for _, c := range content {
		if c.Type != "image" {
			said = append(said, c.Text)

			continue
		}

		data, err := base64.StdEncoding.DecodeString(c.Data)
		if err != nil {
			return nil, fmt.Errorf("the screenshot is not base64: %w", err)
		}

		if err := writeShot(dir, rel, format, data); err != nil {
			return nil, err
		}

		return Text("Screenshot saved to: "+filepath.Join(dir, rel), false), nil
	}

	return Text(strings.TrimSpace(strings.Join(append(said, "No picture came back, so nothing was saved to "+
		asked+"."), "\n")), true), nil
}

// shotPath is where a screenshot asked for at name is written, relative to
// dir, and the format its extension asks for; a path out of dir is refused.
func shotPath(dir, name string) (string, string, error) {
	format, ok := shotFormats[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return "", "", fmt.Errorf("save_to ends in .png, .jpg or .jpeg, not %q", filepath.Ext(name))
	}

	rel := filepath.Clean(name)
	if filepath.IsAbs(name) {
		rel = ""

		// The folder as named, or as its links resolve: an absolute path may name it either way.
		resolved, _ := filepath.EvalSymlinks(dir)
		for _, base := range []string{dir, resolved} {
			if r, err := filepath.Rel(base, name); base != "" && err == nil && filepath.IsLocal(r) {
				rel = r

				break
			}
		}
	}

	if !filepath.IsLocal(rel) || !resolvesInside(dir, rel) {
		return "", "", fmt.Errorf("save_to is a path in the folder you work in (%s)", dir)
	}

	return rel, format, nil
}

// maxLinkHops bounds how many links resolvesInside follows, mobile-mcp's
// MAX_SYMLINK_HOPS (src/utils.ts:93).
const maxLinkHops = 64

// resolvesInside says whether rel is still in dir with its links followed,
// as mobile-mcp follows them before a write (src/utils.ts:95-142): a link at
// the name, even to a file not there yet, is followed to its target, and the
// target's folder is resolved as far as it exists. So a link out of the folder
// is refused before the screen is taken. The write itself is held in the
// folder by os.Root (writeShot).
func resolvesInside(dir, rel string) bool {
	top, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}

	at := filepath.Join(dir, rel)

	for range maxLinkHops {
		info, err := os.Lstat(at)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return existingInside(top, at)
		}

		target, err := os.Readlink(at)
		if err != nil {
			return false
		}

		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(at), target)
		}

		at = target
	}

	return false
}

// existingInside says whether at, resolved as far as it exists, is in top.
func existingInside(top, at string) bool {
	for ; ; at = filepath.Dir(at) {
		if resolved, err := filepath.EvalSymlinks(at); err == nil {
			within, err := filepath.Rel(top, resolved)

			return err == nil && filepath.IsLocal(within)
		}

		if at == filepath.Dir(at) {
			return false
		}
	}
}

// writeShot writes a picture at rel in dir, in the format asked: as it came
// when it is in that format, else converted.
func writeShot(dir, rel, format string, data []byte) error {
	if http.DetectContentType(data) != format {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("the screenshot is not a picture: %w", err)
		}

		var out bytes.Buffer
		if format == "image/png" {
			err = png.Encode(&out, img)
		} else {
			err = jpeg.Encode(&out, img, &jpeg.Options{Quality: shotQuality})
		}

		if err != nil {
			return fmt.Errorf("converting the screenshot: %w", err)
		}

		data = out.Bytes()
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("opening the folder you work in: %w", err)
	}

	err = root.MkdirAll(filepath.Dir(rel), 0o750)
	if err != nil {
		err = fmt.Errorf("making the folder of %s: %w", rel, err)
	} else {
		err = replaceIn(root, rel, data)
	}

	return errors.Join(err, root.Close())
}

// replaceIn writes data beside rel, in a new file of an unguessable name, and
// renames it over rel, as mobile-mcp's writeOutputFile does (src/utils.ts:165-185):
// what is at rel, a link or a hard link of a file elsewhere, is replaced rather
// than written through. A regular file there passes its mode on.
func replaceIn(root *os.Root, rel string, data []byte) error {
	partial := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+"."+rand.Text()+".partial")

	f, err := root.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", rel, err)
	}

	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if info, lerr := root.Lstat(rel); err == nil && lerr == nil && info.Mode().IsRegular() {
		err = root.Chmod(partial, info.Mode().Perm())
	}

	if err == nil {
		err = root.Rename(partial, rel)
	}

	if err != nil {
		_ = root.Remove(partial)

		return fmt.Errorf("writing %s: %w", rel, err)
	}

	return nil
}

// ShotRoute is the route and the wait of a tool that takes save_to: the phone's screen and the browser's
// screenshot. ok is false for any other tool.
func ShotRoute(tool string) (path string, within time.Duration, ok bool) {
	switch tool {
	case devicetool.Screen:
		return agenttool.PathDeviceCall, DeviceCallTimeout, true
	case agenttool.ToolBrowserScreenshot:
		return agenttool.PathToolsCall, CallTimeout, true
	default:
		return "", 0, false
	}
}
