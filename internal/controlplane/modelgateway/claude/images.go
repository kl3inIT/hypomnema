package claude

import (
	"bytes"
	"encoding/json"
	"strings"
)

// OmittedImage is the text put where an image was taken out of a request.
const OmittedImage = "omitted image: the model's provider could not process it; " +
	"read it from its path with a command if you need it"

// imageRefusals are what a provider's 400 says when it could not read an
// image it was sent, lowercased: Anthropic's own, and the two Hermes matches
// as a corrupt image (agent/message_sanitization.py, _IMAGE_CORRUPT_PHRASES).
var imageRefusals = []string{
	"could not process image",
	"image data you provided does not represent a valid image",
	"failed to decode image",
}

// RefusesImage says a provider's refusal is about an image it could not read.
func RefusesImage(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, phrase := range imageRefusals {
		if strings.Contains(lower, phrase) {
			return true
		}
	}

	return false
}

// WithoutImages is a Messages request with every image block, in a message
// or in a tool_result, replaced by a text block saying it was omitted (issue
// #796). An image the provider refused stays in the client's history and
// would be sent again on every turn; Hermes strips the images of the call
// and retries (agent/turn_recovery.py, _strip_request_images_and_retry), and
// OpenClaw leaves a text line where it omits one (tool-images.ts). ok is
// false when the request holds no image or is not a Messages body.
func WithoutImages(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var body item
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, false
	}

	messages, _ := body["messages"].([]any)
	found := false

	for _, m := range messages {
		message, _ := m.(item)
		if message == nil {
			continue
		}

		if blocks, ok := message["content"].([]any); ok {
			found = omitImages(blocks) || found
		}
	}

	if !found {
		return nil, false
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, false
	}

	return encoded, true
}

// omitImages replaces the image blocks of a content list in place, and those
// of the tool results in it.
func omitImages(blocks []any) bool {
	found := false

	for i, b := range blocks {
		block, _ := b.(item)
		if block == nil {
			continue
		}

		switch block["type"] {
		case "image":
			text := item{"type": "text", "text": OmittedImage}
			if cache, ok := block["cache_control"]; ok {
				text["cache_control"] = cache // A breakpoint on the image stays where it was.
			}

			blocks[i] = text
			found = true
		case "tool_result":
			if inner, ok := block["content"].([]any); ok {
				found = omitImages(inner) || found
			}
		}
	}

	return found
}
