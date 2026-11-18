package agenttool

import (
	"fmt"
	"strconv"
)

// PathMemory changes a worker's memory (issue #21): an entry of the memory of
// the agent its Dispatch runs as, or of the one about the owner. Its answer
// is a MemoryAnswer. Kept for a node of a release before the control plane
// listed ToolMemory on PathTools (issue #536), whose tools server defines the
// tool itself and calls here.
const PathMemory = "/agent/v1/memory"

// ToolMemory is the memory's tool (issues #21, #536), one definition in the
// control plane for a worker and a Bot's pi, its arguments a MemoryChange.
const ToolMemory = "memory"

// Targets of a MemoryChange: a worker's own is MemoryAgent, the memory of the
// agent it runs as; a Bot's is MemoryBot, its own; both may write MemoryOwner.
const (
	MemoryAgent = "agent"
	MemoryBot   = "bot"
	MemoryOwner = "owner"
)

// MemoryChange is the body of PathMemory and the arguments of ToolMemory: one
// change, as a Bot's memory takes it (add, replace or remove; Match is part
// of the one entry to replace or remove).
type MemoryChange struct {
	Target string `json:"target"`
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	Match  string `json:"match,omitempty"`
}

// MemoryAnswer is what PathMemory gives the worker: the memory after the
// change, and how full it is.
type MemoryAnswer struct {
	Memory string `json:"memory"`
	Used   int    `json:"used"`
	Limit  int    `json:"limit"`
}

// From this share of its limit a memory's writer is asked to make room, and to
// what share: Coder's 80% and 70% (coderd/x/chatd/chattool/memory.go,
// MemoryConsolidateThreshold), which follow the nudge Claude Code gives.
const (
	memoryFullPercent   = 80
	memoryTargetPercent = 70
)

// MemoryUsage is the line a change's result gives its writer: how full the
// memory is, as Hermes's memory tool says in every result (usage), and from
// 80% a request to make room, as Coder's consolidateNudge. It goes to the
// model only, in a tool's result, never into a prompt the provider caches.
func MemoryUsage(used, limit int) string {
	if limit <= 0 {
		return ""
	}

	percent := used * 100 / limit
	line := fmt.Sprintf("memory: %s of %s characters (%d%%)", thousands(used), thousands(limit), percent)

	if percent >= memoryFullPercent {
		line += fmt.Sprintf(". It is nearly full: merge entries that say the same thing and remove stale or wrong "+
			"ones now, until it is under %d%% (%s characters).", memoryTargetPercent,
			thousands(limit*memoryTargetPercent/100))
	}

	return line
}

// thousands writes n with commas: 2,200.
func thousands(n int) string {
	s := strconv.Itoa(n)

	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}

	return s
}

// MemoryNote is a note of a memory (issue #589) as the start of a chat or a
// task and `hypomnema memory ls` list it: its path and size, as the memory
// tool's view writes it.
func MemoryNote(path string, bytes int) string {
	return path + " (" + HumanSize(bytes) + ")"
}

// HumanSize is a size as du -h writes it, which Anthropic's memory tool's view
// shows: 512B, 1.5K.
func HumanSize(n int) string {
	if n < 1024 {
		return strconv.Itoa(n) + "B"
	}

	return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + "K"
}
