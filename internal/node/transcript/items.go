package transcript

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Fit cuts the items of one line so that together they fit one batch, and
// each fits the bound of an item: a long text, and the long strings of a
// tool's input, are cut with a note of how much was left out.
func Fit(items []nodeproto.Item) []nodeproto.Item {
	// Room for the envelope and each item's field names.
	limit := min(nodeproto.MaxItemBytes, (nodeproto.MaxBatchBytes-4096)/max(len(items), 1))
	for i := range items {
		bound(&items[i], limit)
	}

	return items
}

// bound cuts an item until it encodes to at most limit bytes: its text, and
// the long strings of its input, as Omnigent's forwarder cuts an oversized
// item (harnesses/claude_native/forwarder.py at cf1021e).
func bound(it *nodeproto.Item, limit int) {
	for most := limit; ; most /= 2 {
		c := *it
		c.Text = cut(it.Text, most)
		c.Input = cutInput(it.Input, most)

		if raw, err := json.Marshal(c); (err == nil && len(raw) <= limit) || most < 64 {
			*it = c

			return
		}
	}
}

// cutInput cuts the strings of a tool's input so that it takes about most
// bytes; an input that cannot be cut that far is dropped and marked.
func cutInput(raw json.RawMessage, most int) json.RawMessage {
	if len(raw) <= most {
		return raw
	}

	var input map[string]any
	if most < 64 || json.Unmarshal(raw, &input) != nil {
		return json.RawMessage(`{"truncated":true}`)
	}

	out, err := json.Marshal(cutStrings(input, most/8))
	if err != nil {
		return json.RawMessage(`{"truncated":true}`)
	}

	return out
}

func cutStrings(v any, most int) any {
	switch x := v.(type) {
	case string:
		return cut(x, most)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cutStrings(e, most)
		}

		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cutStrings(e, most)
		}

		return out
	default:
		return v
	}
}

// cut keeps the first most bytes of s, on a rune boundary, and says how much
// it left out.
func cut(s string, most int) string {
	most = max(most, 64)
	if len(s) <= most {
		return s
	}

	keep := most
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}

	return s[:keep] + fmt.Sprintf("\n… [%d more bytes not kept]", len(s)-keep)
}
