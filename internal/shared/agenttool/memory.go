package agenttool

import (
	"fmt"
	"strconv"
)

// PathMemory changes a worker's memory (issue #21): an entry of the memory of
// the agent its Dispatch runs as, or of the one about the owner. Its answer
// is a MemoryAnswer.
const PathMemory = "/agent/v1/memory"

// Targets of a MemoryChange.
const (
	MemoryAgent = "agent"
	MemoryOwner = "owner"
)

// MemoryChange is the body of PathMemory: one change, as a Bot's memory takes
// it (add, replace or remove; Match is part of the one entry to replace or
// remove).
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
