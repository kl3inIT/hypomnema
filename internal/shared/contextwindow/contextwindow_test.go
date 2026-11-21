package contextwindow_test

import (
	"testing"

	"github.com/kl3inIT/hypomnema/internal/shared/contextwindow"
)

func TestExceededKnowsAnOverflowFromItsText(t *testing.T) {
	for text, want := range map[string]bool{
		"Codex ran out of room in the model's context window. Start a new thread or clear earlier history " +
			"before retrying.": true,
		"context_length_exceeded: Your input exceeds the context window of this model.": true,
		"prompt is too long: 210000 tokens > 200000 maximum":                            true,
		"Rate limit reached: 30000 tokens per minute (TPM); context length is fine":     false,
		"input is too long for requested model":                                         true,
		"maximum context length is 128000 tokens":                                       true,
		"invalid_value: Tool input is too long":                                         false,
		"Input is too long: shorten the tool's arguments":                               false,
		"usage limit reached":                                                           false,
		"stream disconnected":                                                           false,
		"model_context_window too high":                                                 false,
		"":                                                                              false,
	} {
		if got := contextwindow.Exceeded(text); got != want {
			t.Errorf("Exceeded(%q) = %v, want %v", text, got, want)
		}
	}
}
