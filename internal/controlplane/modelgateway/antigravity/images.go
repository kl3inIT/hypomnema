package antigravity

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Images made under an Antigravity account (issue #431): one generateContent
// request in the envelope the IDE sends for an image, requestType image_gen,
// its imageConfig the aspect ratio and the size, each reference image as
// inlineData before the prompt. It is OmniRoute's
// (open-sse/handlers/imageGeneration.ts handleGeminiImageGeneration and
// imageGeneration/providers/geminiInline.ts), which 9router-go
// (internal/handlers/media/antigravity_image.go,
// internal/translator/antigravity.go WrapAntigravityImageRequest) and omp
// (packages/ai/src/images/google-antigravity.ts) agree with in its parts.

// ImageModel is the model an Antigravity image is made with when nothing
// else is named: 9router-go's default and OmniRoute's.
const ImageModel = "gemini-3.1-flash-image"

// MaxImageReferences is how many reference images one request may carry
// (OmniRoute's GEMINI_IMAGE_MAX_REFERENCES).
const MaxImageReferences = 14

// ImageInput is a reference image.
type ImageInput struct {
	MIME string
	Data []byte
}

// ImageOptions are what an image request may set beside its prompt.
type ImageOptions struct {
	Model string
	// Count is how many images: the candidate count.
	Count int
	// Size is the OpenAI size (1024x1536) or an aspect ratio (2:3); AspectRatio,
	// when given, wins (OmniRoute's normalizeImageAspectRatio).
	Size, AspectRatio string
	// ImageSize is 1K, 2K or 4K; empty leaves Google's default.
	ImageSize string
	Images    []ImageInput
	// Project and RequestID are the envelope's.
	Project, RequestID string
}

// The OpenAI sizes as aspect ratios (OmniRoute's translator/image/sizeMapper.ts).
var sizeRatios = map[string]string{
	"256x256": "1:1", "512x512": "1:1", "1024x1024": "1:1",
	"1792x1024": "16:9", "1024x1792": "9:16", "1536x1024": "3:2", "1024x1536": "2:3",
}

var ratioPattern = regexp.MustCompile(`^\d+:\d+$`)

// AspectRatio is a request's aspect ratio: the one it names, else its size's,
// else 1:1, as OmniRoute's mapImageSize.
func AspectRatio(ratio, size string) string {
	if r := strings.TrimSpace(ratio); ratioPattern.MatchString(r) {
		return r
	}

	size = strings.TrimSpace(size)
	if ratioPattern.MatchString(size) {
		return size
	}

	if r, ok := sizeRatios[size]; ok {
		return r
	}

	return "1:1"
}

// ImageSize is the output size Google takes, 1K, 2K or 4K, or empty for its
// default; anything else is 1K, as OmniRoute's normalizeImageGenerationSize
// clamps it.
func ImageSize(size string) string {
	switch s := strings.ToUpper(strings.TrimSpace(size)); s {
	case "":
		return ""
	case "1K", "2K", "4K":
		return s
	default:
		return "1K"
	}
}

// imagePrompt asks for an image that uses every reference, when there are
// some (OmniRoute's buildGeminiImagePrompt).
func imagePrompt(prompt string, refs int) string {
	if refs == 0 {
		return prompt
	}

	noun := "reference images"
	if refs == 1 {
		noun = "reference image"
	}

	return fmt.Sprintf("Generate an image as the final response. Use all %d attached %s in the result. "+
		"Do not answer with text only.\n\n%s", refs, noun, prompt)
}

// ImageBody is the request for an image of prompt.
func ImageBody(prompt string, o ImageOptions) []byte {
	parts := make([]any, 0, len(o.Images)+1)
	for _, img := range o.Images {
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}

		parts = append(parts, map[string]any{"inlineData": map[string]string{
			"mimeType": mime, "data": base64.StdEncoding.EncodeToString(img.Data),
		}})
	}

	parts = append(parts, map[string]any{"text": imagePrompt(prompt, len(o.Images))})

	imageConfig := map[string]string{"aspectRatio": AspectRatio(o.AspectRatio, o.Size)}
	if size := ImageSize(o.ImageSize); size != "" {
		imageConfig["imageSize"] = size
	}

	body, _ := json.Marshal(map[string]any{ //nolint:errchkjson // Strings, numbers and maps of them always encode.
		"project":   o.Project,
		"requestId": o.RequestID,
		"request": map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": parts}},
			"generationConfig": map[string]any{
				"candidateCount": max(o.Count, 1),
				"imageConfig":    imageConfig,
			},
		},
		"model":       o.Model,
		"userAgent":   "antigravity",
		"requestType": "image_gen",
	})

	return body
}

// Image is one image of an answer.
type Image struct {
	B64JSON, MIME, RevisedPrompt string
}

// ImageAnswer is an answer's images and its token counts.
type ImageAnswer struct {
	Images       []Image
	InputTokens  int64
	OutputTokens int64
}

// RefusedError is an answer that holds no image: its text, when the model
// wrote some, is why (OmniRoute's extractGeminiImageResults).
type RefusedError struct{ Text string }

func (e *RefusedError) Error() string {
	if e.Text == "" {
		return "Antigravity did not return an image"
	}

	return "Antigravity returned text instead of an image: " + e.Text
}

// ParseImages reads an image answer: every inlineData part of every candidate
// is an image, its revised prompt the candidate's first text, else the prompt.
// The backend wraps the Gemini answer in {"response": …}, or does not.
func ParseImages(raw []byte, prompt string) (ImageAnswer, error) {
	type part struct {
		Text       string `json:"text"`
		InlineData *struct {
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		} `json:"inlineData"`
	}

	type answer struct {
		Candidates []struct {
			Content struct {
				Parts []part `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}

	var envelope struct {
		Response *answer `json:"response"`
		answer
	}

	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ImageAnswer{}, fmt.Errorf("reading Antigravity's image: %w", err)
	}

	got := envelope.answer
	if envelope.Response != nil {
		got = *envelope.Response
	}

	var (
		out   ImageAnswer
		texts []string
	)

	for _, c := range got.Candidates {
		revised := ""

		for _, p := range c.Content.Parts {
			if p.Text != "" && revised == "" {
				revised = p.Text
			}
		}

		if revised == "" {
			revised = prompt
		}

		for _, p := range c.Content.Parts {
			switch {
			case p.InlineData != nil && p.InlineData.Data != "":
				out.Images = append(out.Images, Image{
					B64JSON: p.InlineData.Data, MIME: p.InlineData.MimeType, RevisedPrompt: revised,
				})
			case strings.TrimSpace(p.Text) != "":
				texts = append(texts, strings.TrimSpace(p.Text))
			}
		}
	}

	if got.UsageMetadata != nil {
		out.InputTokens, out.OutputTokens = got.UsageMetadata.PromptTokenCount, got.UsageMetadata.CandidatesTokenCount
	}

	if len(out.Images) == 0 {
		text := strings.Join(texts, " ")
		if len(text) > 300 {
			text = text[:300]
		}

		return out, &RefusedError{Text: text}
	}

	return out, nil
}

// QuotaResetAt is the moment a refusal says the quota comes back, in its
// details' quotaResetTimeStamp (9router-go's geminiQuotaResetSeconds), or
// the zero time.
func QuotaResetAt(raw []byte) time.Time {
	var parsed struct {
		Error struct {
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}

	if json.Unmarshal(raw, &parsed) != nil {
		return time.Time{}
	}

	for _, d := range parsed.Error.Details {
		if at, err := time.Parse(time.RFC3339, d.Metadata["quotaResetTimeStamp"]); err == nil {
			return at
		}
	}

	return time.Time{}
}

// ImageRequestID is the envelope's request id for an image: OmniRoute's
// image_gen/<ms>/<id>/0.
func ImageRequestID(now time.Time, id string) string {
	return "image_gen/" + strconv.FormatInt(now.UnixMilli(), 10) + "/" + id + "/0"
}
