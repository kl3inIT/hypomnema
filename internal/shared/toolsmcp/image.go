package toolsmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// generate_image (issue #492) is the control plane's, as the web's tools
// are, but its files are the agent's machine's: the images to edit are read
// here and sent whole, and the image made is saved here and its path given,
// as opencode's experimental_image_generate reads the paths it is given and
// saves to the path asked or a new temporary file
// (core/src/tool/plugin/image-generate.ts), and omp's generate_image saves to
// a temporary file and says where (src/tools/image-gen.ts). The agent reads
// the file to see the image, as with both: the path is the answer, not the
// image.
const (
	// ImageCallTimeout is longer than the gateway may take: each of its two
	// providers is given five minutes for an image (Hermes waits 300 s).
	ImageCallTimeout = 11 * time.Minute
	// maxImageAnswerBytes bounds an answer with an image: one image in
	// base64, a large one at 4K.
	maxImageAnswerBytes = 64 << 20
)

// Images says where generate_image asks and saves.
type Images struct {
	// Path is the agent API's route of the call: PathBrowserCall for a
	// worker, PathGatewayCall for a Bot.
	Path string
	// Folder, when set, is where an image is saved when no path is asked, and
	// the answer gives a saved image in it as the MEDIA:<path> line, relative
	// to the folder the agent works in, that sends it into the chat
	// (controlplane/channels/media.go): a Bot's pi, whose /tmp is its box's
	// own and whose chat sends only files of its folder. Empty: a new file in
	// the temporary folder, and the path alone.
	Folder string
}

// imageTypes are the images the providers take, with their files'
// extensions, as opencode's image tool's INPUT_TYPES and EXTENSIONS.
var imageTypes = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif",
}

// ImageCall reads the images named, asks the control plane for the image,
// and saves it. What the agent can fix is its error.
func (a *Agent) ImageCall(ctx context.Context, arguments json.RawMessage, where Images) (any, error) {
	var in struct {
		Prompt  string   `json:"prompt"`
		Images  []string `json:"images"`
		Path    string   `json:"path"`
		Size    string   `json:"size"`
		Quality string   `json:"quality"`
	}

	if len(arguments) > 0 && json.Unmarshal(arguments, &in) != nil {
		return nil, errors.New("generate_image takes {prompt, images, path, size, quality}")
	}

	if len(in.Images) > agenttool.MaxImageInputs {
		return nil, fmt.Errorf("at most %d images can be given", agenttool.MaxImageInputs)
	}

	call := agenttool.ImageCall{Prompt: in.Prompt, Size: in.Size, Quality: in.Quality}
	sent := 0

	for _, name := range in.Images {
		img, err := readImage(name)
		if err != nil {
			return nil, err
		}

		sent += base64.StdEncoding.EncodedLen(len(img.Data))
		call.Images = append(call.Images, img)
	}

	if sent > agenttool.MaxCallBytes {
		return nil, fmt.Errorf("the images are too large together: at most %d MiB once encoded",
			agenttool.MaxCallBytes>>20)
	}

	args, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}

	raw, err := a.toolCall(ctx, where.Path, ImageCallTimeout, maxImageAnswerBytes, agenttool.ToolGenerateImage, args)
	if err != nil {
		return nil, err
	}

	var result struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}

	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("the control plane's answer could not be read")
	}

	if result.IsError {
		return raw, nil
	}

	var (
		lines []string
		saved int
	)

	for _, c := range result.Content {
		switch c.Type {
		case "text":
			lines = append(lines, c.Text)
		case "image":
			path, err := saveImage(in.Path, where.Folder, saved, c.Data, c.MimeType)
			if err != nil {
				return nil, err
			}

			saved++

			lines = append(lines, "Saved to "+path)
			if where.Folder != "" {
				lines = append(lines, mediaLine(path))
			}
		}
	}

	if saved == 0 {
		return nil, errors.New("the control plane answered without an image")
	}

	return Text(strings.Join(lines, "\n"), false), nil
}

// mediaLine says how a Bot sends a saved image into its chat: the MEDIA line
// with its path relative to the folder it works in, the only folder whose
// files its chat sends (OpenMausBot's generate_image says the image "is
// attached to your reply"; here the reply attaches it).
func mediaLine(path string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "To send it into the chat, put this line on a line of its own in your reply, outside a code " +
				"block: MEDIA:" + filepath.ToSlash(rel)
		}
	}

	return "It is outside the folder you work in, so it cannot be sent into the chat: save it in your folder to " +
		"send it."
}

// readImage reads an image file to send, with opencode's checks and words.
func readImage(name string) (agenttool.ImageInput, error) {
	info, err := os.Stat(name)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return agenttool.ImageInput{}, fmt.Errorf("file not found: %s", name)
	case err != nil:
		return agenttool.ImageInput{}, fmt.Errorf("reading %s: %w", name, err)
	case !info.Mode().IsRegular():
		return agenttool.ImageInput{}, fmt.Errorf("%s is not a file", name)
	case info.Size() > agenttool.MaxImageInputBytes:
		return agenttool.ImageInput{}, fmt.Errorf("%s is larger than %d MiB", name, agenttool.MaxImageInputBytes>>20)
	}

	data, err := os.ReadFile(name) //nolint:gosec // the agent names the image it wants sent
	if err != nil {
		return agenttool.ImageInput{}, fmt.Errorf("reading %s: %w", name, err)
	}

	mime := http.DetectContentType(data)
	if _, ok := imageTypes[mime]; !ok {
		return agenttool.ImageInput{}, fmt.Errorf("%s is not a PNG, JPEG, WebP or GIF image", name)
	}

	return agenttool.ImageInput{MIME: mime, Data: data}, nil
}

// saveImage writes the nth image of an answer and returns its absolute
// path: at the path asked, its extension the image's real one (opencode's
// image tool does the same) and numbered after the first, or a new file in
// folder, the temporary folder when it is empty.
func saveImage(asked, folder string, n int, b64, mime string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("the image is not base64: %w", err)
	}

	ext, ok := imageTypes[http.DetectContentType(data)]
	if !ok {
		ext, ok = imageTypes[mime]
	}

	if !ok {
		ext = ".png"
	}

	if asked == "" {
		prefix := "hypomnema-image-*"
		if folder != "" {
			prefix = "image-*"

			if err := os.MkdirAll(folder, 0o750); err != nil {
				return "", fmt.Errorf("making %s: %w", folder, err)
			}
		}

		f, err := os.CreateTemp(folder, prefix+ext)
		if err != nil {
			return "", fmt.Errorf("making the image's file: %w", err)
		}

		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}

		if err != nil {
			return "", fmt.Errorf("writing %s: %w", f.Name(), err)
		}

		return filepath.Abs(f.Name())
	}

	path, err := filepath.Abs(asked)
	if err != nil {
		return "", fmt.Errorf("the path %s: %w", asked, err)
	}

	path = strings.TrimSuffix(path, filepath.Ext(path))
	if n > 0 {
		path += fmt.Sprintf("-%d", n+1)
	}

	path += ext

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}

	return path, nil
}
