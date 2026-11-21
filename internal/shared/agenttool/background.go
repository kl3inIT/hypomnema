package agenttool

// PathBackground is where a Bot's pi says what its background commands do (issue #1058): the box's pi-background
// extension posts a BackgroundReport when one starts and when it ends, with pi's own token. The control plane keeps
// the running ones and wakes the chat when one ends, as OpenClaw's exec continues the conversation when a
// backgrounded command exits (docs/tools/exec.md:178).
const PathBackground = "/agent/v1/background"

// The kinds of BackgroundReport.
const (
	BackgroundStarted = "started"
	BackgroundEnded   = "ended"
)

// BackgroundReport is one report of a background command. An ended one carries its exit code or the signal that
// killed it, and its output's end; it was started by the pi that reports it.
type BackgroundReport struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// ExitCode is set when it exited, Signal when a signal ended it.
	ExitCode *int   `json:"exit_code,omitempty"`
	Signal   string `json:"signal,omitempty"`
	// Output is the end of its output, at most 2,000 characters; OutputChars how many it wrote in all.
	Output      string `json:"output,omitempty"`
	OutputChars int64  `json:"output_chars,omitempty"`
	// OutputFile has all its output in the box, when Output is only its end; Dropped says output past the file's
	// limit was discarded.
	OutputFile string `json:"output_file,omitempty"`
	Dropped    bool   `json:"dropped,omitempty"`
}
