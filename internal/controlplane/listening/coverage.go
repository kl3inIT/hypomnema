package listening

import "slices"

// What a source covered, as the answer says it: read with items, read and empty, read in part, or not read. Only a
// clean zero says nobody talked: a source that failed is "not read", with its cause, never a zero (last30days
// references/synthesis.md and lib/schema.py:505-554; last30days-cn SKILL.md §5: "⚠️ 0" only when the search worked;
// trend-opportunity-radar sampling-contract.md: a zero is evidence about the query, a block is not a zero).
const (
	StateOK        = "ok"
	StatePartial   = "partial"
	StateNoResults = "no_results"
	StateNotRead   = "not_read"
)

// Coverage is one source's line of the answer.
type Coverage struct {
	Source string `json:"source"`
	// State is StateOK, StatePartial, StateNoResults or StateNotRead.
	State string `json:"state"`
	// Items are those read in the window, before duplicates are removed.
	Items int `json:"items"`
	// Unverified counts what a model said it found that its links did not bear out (Answer.Unverified).
	Unverified int `json:"unverified,omitempty"`
	// Code is why a stream was not read, the most actionable of them (last30days FAILURE_SPECIFICITY); empty when
	// every stream was read.
	Code    Code     `json:"code,omitempty"`
	Streams []Stream `json:"streams"`
}

// Stream is one subquery's reading of the source: the backend that served, or why none did, and each tried.
type Stream struct {
	Subquery string `json:"subquery"`
	ServedBy string `json:"served_by,omitempty"`
	Items    int    `json:"items"`
	// Unverified counts the stream's unverified items (Answer.Unverified).
	Unverified int `json:"unverified,omitempty"`
	// Code is no_results for a clean zero, a failure's code when nothing read it, empty when it was served.
	Code  Code      `json:"code,omitempty"`
	Tried []Attempt `json:"tried"`
}

// read says the stream was read: served, or read and empty.
func (s Stream) read() bool { return s.Code == "" || s.Code == CodeNoResults }

// settle sets the source's state from its streams: ok when every stream was read and some found items, no_results
// when every stream was read and none did, not_read when none was read, partial otherwise (last30days add_items and
// record_failure, lib/schema.py:383-554, which keep a failure beside items as "partial").
func (c Coverage) settle() Coverage {
	var failed []Code

	read := 0

	for _, s := range c.Streams {
		c.Items += s.Items
		c.Unverified += s.Unverified

		if s.read() {
			read++
		} else {
			failed = append(failed, s.Code)
		}
	}

	switch {
	case len(failed) == 0 && c.Items > 0:
		c.State = StateOK
	case len(failed) == 0:
		c.State = StateNoResults
	case read == 0 && c.Items == 0:
		c.State = StateNotRead
	default:
		c.State = StatePartial
	}

	if len(failed) > 0 {
		c.Code = slices.MinFunc(failed, func(a, b Code) int { return specificity(a) - specificity(b) })
	}

	return c
}
