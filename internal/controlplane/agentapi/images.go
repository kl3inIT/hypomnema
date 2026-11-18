package agentapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// An agent's generate_image (issue #492): the gateway's Images
// (modelgateway/images.go), which a Routine's script reaches through
// `hypomnema image`, given to every worker runtime and to a Bot's pi as one
// tool of Hypomnema's MCP server (internal/shared/toolsmcp), as
// OpenMausBot's generate_image is one of its `agents` server
// (server/drivers/agents-image-tools.ts) and Hermes' image_generate one of
// its tools server for Codex. It makes one image a call, as both do and as
// opencode's image tool does; the files are the agent's machine's to read
// and write (agenttool.ToolGenerateImage).

// Images makes images for an agent; the control plane's wiring records each
// call on the gateway key of who asked, as Web does.
type Images interface {
	Make(ctx context.Context, who Who, req modelgateway.ImageRequest) (modelgateway.ImageResult, error)
}

// WithImages gives workers and Bots generate_image.
func (s *Server) WithImages(images Images) *Server {
	s.images = images

	return s
}

// maxPromptChars bounds a prompt, as OpenMausBot's IMAGE_PROMPT_MAX_CHARS.
const maxPromptChars = 4000

// imageTool is generate_image's definition. Its words are OpenMausBot's and
// opencode's (experimental_image_generate's description and input), with
// what the `images` skill told a Bot until the tool replaced it (issue #492).
// A Bot's says the image is saved in its folder and sent into its chat with
// the MEDIA line its answer gives, as OpenMausBot's says the image "is
// attached to your reply" (server/drivers/agents-image-tools.ts).
func imageTool(bot bool) agenttool.BrowserTool {
	saved := "The image is saved as a file and its path given; read the file to look at it. "
	where := "A new file in the temporary folder when left out."

	if bot {
		saved = "The image is saved as a file in your folder and its path given, with the MEDIA line that sends " +
			"it into the chat: put that line in your reply. Look at the file before you send it, when you can " +
			"read images. Each image spends the owner's subscription: make one unless asked for more. "
		where = "A new file under images/ in your folder when left out; only a file in your folder can be sent " +
			"into the chat."
	}

	return agenttool.BrowserTool{
		Name: agenttool.ToolGenerateImage,
		Description: "Make one image from a description, or edit the images you name, through the owner's image " +
			"subscriptions. " + saved +
			"Use it only when the task asks for an image; for several, call it once an image. To look at an image " +
			"you have, read its file instead. It takes a minute " +
			"or so: do not repeat a call that is still running. Write the prompt in English, concrete and visual: " +
			"the subject, the style, the composition, the light, and any text that must appear, word for word. " +
			"To edit, name the image files and say what to keep as well as what to change. A rate limit says " +
			"when the providers can be asked again: do not retry in a loop.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` +
			`"prompt":{"type":"string","minLength":1,"maxLength":4000,"description":"What to make, or for an ` +
			`edit, what to change: subject, setting, style, composition, light, and any text in the image."},` +
			`"images":{"type":"array","items":{"type":"string","minLength":1},"maxItems":8,"description":` +
			`"Image files to edit, or to use as references for style and subject, in order: paths on this ` +
			`machine, absolute or relative to the folder you work in. PNG, JPEG, WebP or GIF, each up to 20 MiB."},` +
			`"path":{"type":"string","description":"Where to save the image, absolute or relative to the folder ` +
			`you work in; its extension becomes the image's real one, and a file already there is replaced. ` +
			where + `"},` +
			`"size":{"type":"string","description":"1024x1024 (square), 1536x1024 (landscape), 1024x1536 ` +
			`(portrait), auto, or an aspect ratio such as 16:9. The provider's default when left out."},` +
			`"quality":{"type":"string","enum":["low","medium","high","auto"],"description":"How much detail; ` +
			`only Codex's images take it."}},"required":["prompt"]}`),
	}
}

// imageCall makes the image and answers with what made it, as text, and the
// image, as MCP image content. What the request got wrong and why no provider
// made it are the tool's error, for the agent to read; anything else is the
// control plane's failure.
func (s *Server) imageCall(w http.ResponseWriter, r *http.Request, a agent, call agenttool.BrowserCall) {
	// An image takes a minute or more, longer than the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	var in agenttool.ImageCall
	if json.Unmarshal(orEmpty(call.Arguments), &in) != nil {
		refuse(w, http.StatusBadRequest, "generate_image takes {prompt, size, quality, images: [{mime_type, data}]}")

		return
	}

	if len([]rune(in.Prompt)) > maxPromptChars {
		answerTool(w, "the prompt is longer than 4000 characters", true)

		return
	}

	if len(in.Images) > agenttool.MaxImageInputs {
		answerTool(w, "at most 8 images can be given", true)

		return
	}

	req := modelgateway.ImageRequest{Prompt: in.Prompt, N: 1, Size: in.Size, Quality: in.Quality}
	for _, img := range in.Images {
		req.Images = append(req.Images, modelgateway.ImageInput{MIME: img.MIME, Data: img.Data})
	}

	res, err := s.images.Make(r.Context(), a.who(), req)

	// Which agent made an image, and never what it asked for.
	s.logger.InfoContext(r.Context(), "an agent made an image",
		slog.String("event", "agentapi.image_call"), slog.String("agent", a.name), slog.String("tool", call.Tool))

	var failed *modelgateway.ImageError

	switch {
	case err == nil:
	case errors.As(err, &failed), errors.Is(err, modelgateway.ErrInvalid):
		answerTool(w, err.Error(), true)

		return
	default:
		s.fail(w, r, err)

		return
	}

	content := []map[string]string{{"type": "text", "text": imageText(res)}}

	for _, img := range res.Images {
		// The gateway's providers answer base64; an answer that is not is no image.
		if _, err := base64.StdEncoding.DecodeString(img.B64JSON); err != nil {
			continue
		}

		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}

		content = append(content, map[string]string{"type": "image", "data": img.B64JSON, "mimeType": mime})
	}

	if len(content) == 1 {
		answerTool(w, "the provider answered without an image", true)

		return
	}

	w.Header().Set("Cache-Control", "no-store")
	answer(w, map[string]any{"content": content, "isError": false})
}

// imageText says what made the image, and what the provider says it made,
// which may not be what was asked (issue #453), as `hypomnema image` says it.
func imageText(res modelgateway.ImageResult) string {
	line := "Made by " + res.Provider + " with " + res.Model
	if res.Size != "" {
		line += " at " + res.Size
	}

	if res.Quality != "" {
		line += ", quality " + res.Quality
	}

	for i, f := range res.Errors {
		sep := ", after "
		if i > 0 {
			sep = "; "
		}

		line += sep + f.Provider + " failed (" + f.Message + ")"
	}

	text := line + "."

	for _, img := range res.Images {
		if p := strings.TrimSpace(img.RevisedPrompt); p != "" {
			text += "\nThe provider's prompt: " + p
		}
	}

	return text
}
