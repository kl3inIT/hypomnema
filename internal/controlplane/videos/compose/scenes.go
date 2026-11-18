package compose

import (
	"errors"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// ErrNoSteps refuses a take with no steps: a video's scenes are its steps.
var ErrNoSteps = errors.New("take has no steps")

// Scene is one step of a take, or steps said at the same time (issue #818,
// row 3), and the span of the take it shows: [StartMs, EndMs) on the take's
// timeline, from its step to the next one, the last to the take's end. The
// first scene starts at the take's start: the span before its step is a
// lead-in, cut as an idle span (aidemo src/compose.ts:209, leadInMs).
type Scene struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
	// StepAtMs is when its step was said; LeadInMs is the span before it, in the first scene only.
	StepAtMs int64 `json:"step_at_ms"`
	LeadInMs int64 `json:"lead_in_ms,omitempty"`
	StartMs  int64 `json:"start_ms"`
	EndMs    int64 `json:"end_ms"`
	// Tap is where the agent was about to tap, in the screen's pixels, at TapAtMs.
	Tap     *devicetool.TakeTap `json:"tap,omitempty"`
	TapAtMs int64               `json:"tap_at_ms,omitempty"`
}

// Scenes are a take's scenes, in order. Steps at the same time are one
// scene, their lines joined, with the first tap among them.
func Scenes(take devicetool.Take) ([]Scene, error) {
	if len(take.Steps) == 0 {
		return nil, ErrNoSteps
	}

	length := TakeLengthMs(take)

	var scenes []Scene

	for _, step := range take.Steps {
		at := min(max(step.AtMs, 0), length)

		if n := len(scenes); n > 0 && scenes[n-1].StepAtMs == at {
			last := &scenes[n-1]
			last.Text = joinLines(last.Text, step.Text)

			if last.Tap == nil && step.Tap != nil {
				last.Tap, last.TapAtMs = step.Tap, at
			}

			continue
		}

		sc := Scene{Index: len(scenes), Text: strings.TrimSpace(step.Text), StepAtMs: at, StartMs: at}
		if step.Tap != nil {
			sc.Tap, sc.TapAtMs = step.Tap, at
		}

		scenes = append(scenes, sc)
	}

	scenes[0].LeadInMs = scenes[0].StepAtMs
	scenes[0].StartMs = 0

	for i := range scenes {
		scenes[i].EndMs = length
		if i+1 < len(scenes) {
			scenes[i].EndMs = scenes[i+1].StartMs
		}
	}

	return scenes, nil
}

// TakeLengthMs is the length of a take's timeline: its recorded length, or
// where its last segment ends when that is later.
func TakeLengthMs(take devicetool.Take) int64 {
	length := take.DurationMs

	for _, seg := range take.Segments {
		length = max(length, seg.AtMs+seg.LengthMs)
	}

	return length
}

func joinLines(a, b string) string {
	b = strings.TrimSpace(b)

	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " " + b
	}
}
