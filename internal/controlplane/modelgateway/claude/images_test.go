package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/claude"
)

// An image a user sent keeps its cache breakpoint on the line that replaces
// it, and the rest of the request is left as it was.
func TestAnOmittedImageKeepsItsPlaceAndBreakpoint(t *testing.T) {
	t.Parallel()

	raw := `{"model":"sol","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"look"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"},` +
		`"cache_control":{"type":"ephemeral"}}]}]}`

	out, ok := claude.WithoutImages([]byte(raw))
	require.True(t, ok)
	assert.JSONEq(t, `{"model":"sol","max_tokens":10,"messages":[{"role":"user","content":[`+
		`{"type":"text","text":"look"},{"type":"text","text":"`+claude.OmittedImage+`",`+
		`"cache_control":{"type":"ephemeral"}}]}]}`, string(out))

	_, ok = claude.WithoutImages([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	assert.False(t, ok, "a request without an image is not asked again")
}

func TestOnlyARefusalAboutAnImageIsOne(t *testing.T) {
	t.Parallel()

	assert.True(t, claude.RefusesImage([]byte(`{"error":{"message":"messages.3.content.0: Could not process image"}}`)))
	assert.True(t, claude.RefusesImage([]byte(`failed to decode image: invalid or unsupported image format`)))
	assert.False(t, claude.RefusesImage([]byte(`{"error":{"message":"max_tokens: too large"}}`)))
}
