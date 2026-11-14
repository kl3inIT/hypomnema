package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Images made under a ChatGPT account (issue #431): the Codex backend's own
// Images routes, POST <backend>/images/generations and /images/edits, which
// the Codex CLI's typed Images client calls (codex-rs
// codex-api/src/endpoint/images.rs, codex-api/src/images.rs) and OmniRoute
// (open-sse/handlers/imageGeneration/providers/codexImages.ts), Hermes
// (plugins/image_gen/openai-codex) and CLIProxyAPI
// (internal/runtime/executor/codex_openai_images.go, the direct routes) call
// the same way. One image a call: n is fanned out by the caller, as Codex,
// OmniRoute and Hermes send n = 1.

// ImageModel is the model a Codex image is made with when nothing else is
// named: the one codex-rs's image extension and Hermes send.
const ImageModel = "gpt-image-2"

// ImageInput is a reference image sent with an edit.
type ImageInput struct {
	MIME string
	Data []byte
}

// ImageOptions are what an image request may set beside its prompt.
type ImageOptions struct {
	Model      string
	Size       string
	Quality    string
	Background string
	Images     []ImageInput
}

// ImagePath is the route a request goes to: edits when it carries images.
func ImagePath(o ImageOptions) string {
	if len(o.Images) > 0 {
		return "/images/edits"
	}

	return "/images/generations"
}

// ImageBody is one call's body: OmniRoute's upstreamBody, the fields of
// codex-rs's ImageGenerationRequest and ImageEditRequest, the references as
// data URLs in images[].image_url.
func ImageBody(prompt string, o ImageOptions) []byte {
	body := map[string]any{"prompt": prompt, "model": o.Model, "n": 1}

	if o.Size != "" {
		body["size"] = o.Size
	}

	if o.Quality != "" {
		body["quality"] = imageQuality(o.Quality)
	}

	if o.Background != "" {
		body["background"] = o.Background
	}

	if len(o.Images) > 0 {
		refs := make([]any, 0, len(o.Images))
		for _, img := range o.Images {
			mime := img.MIME
			if mime == "" {
				mime = "image/png"
			}

			refs = append(refs, map[string]string{
				"image_url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
			})
		}

		body["images"] = refs
	}

	encoded, _ := json.Marshal(body) //nolint:errchkjson // Strings, numbers and slices of them always encode.

	return encoded
}

// imageQuality maps the older clients' words to the backend's (OmniRoute's
// mapLegacyImageQualityToImageTool): standard is medium, hd is high.
func imageQuality(q string) string {
	switch strings.ToLower(q) {
	case "standard":
		return "medium"
	case "hd":
		return "high"
	default:
		return strings.ToLower(q)
	}
}

// ImageUsage is what one call's usage said, in tokens.
type ImageUsage struct {
	Input, Output int64
}

// ImageAnswer is one call's images, base64 as the backend sent them.
type ImageAnswer struct {
	Created int64
	Images  []string
	// Format is the backend's output_format: png, jpeg or webp.
	Format string
	Usage  ImageUsage
}

// ParseImages reads an Images route's answer. One without an image is a
// failure, as OmniRoute's readImagesResponse and codex-rs's
// image_response_requires_image_data treat it.
func ParseImages(raw []byte) (ImageAnswer, error) {
	var parsed struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
		OutputFormat string `json:"output_format"`
		Usage        *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ImageAnswer{}, fmt.Errorf("reading Codex's images: %w", err)
	}

	out := ImageAnswer{Created: parsed.Created, Format: parsed.OutputFormat}

	for _, d := range parsed.Data {
		if d.B64JSON != "" {
			out.Images = append(out.Images, d.B64JSON)
		}
	}

	if len(out.Images) == 0 {
		return ImageAnswer{}, fmt.Errorf("the Codex Images API returned no b64_json image")
	}

	if parsed.Usage != nil {
		out.Usage = ImageUsage{Input: parsed.Usage.InputTokens, Output: parsed.Usage.OutputTokens}
	}

	return out, nil
}

// NotEntitled reports whether a refusal says the account may not use the
// image model: the 400 the backend answers an account whose plan has no
// image model (OmniRoute's isCodexChatGptModelAccessError, issue #8307),
// which another account may serve.
func NotEntitled(status int, raw []byte, model string) bool {
	if status != http.StatusBadRequest {
		return false
	}

	var parsed struct {
		Detail  string `json:"detail"`
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	var detail string

	if json.Unmarshal(raw, &detail) != nil {
		_ = json.Unmarshal(raw, &parsed)

		switch {
		case parsed.Detail != "":
			detail = parsed.Detail
		case parsed.Message != "":
			detail = parsed.Message
		case parsed.Error != nil:
			detail = parsed.Error.Message
		}
	}

	return detail == "The '"+model+"' model is not supported when using Codex with a ChatGPT account."
}

// FreePlan reports whether an account's plan is ChatGPT's free one, which has
// no image model: 9router-go's codexPlanType == "free" and OmniRoute's
// isCodexFreePlan skip it before calling. An unknown plan is not free.
func FreePlan(plan string) bool {
	return strings.EqualFold(strings.TrimSpace(plan), "free")
}
