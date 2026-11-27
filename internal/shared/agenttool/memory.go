package agenttool

import (
	"fmt"
	"strconv"
)

// ToolMemory is the memory's tool (issues #21, #536), one definition in the
// control plane for a worker and a Bot's pi.
const ToolMemory = "memory"

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
