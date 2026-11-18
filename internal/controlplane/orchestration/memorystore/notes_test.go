package memorystore_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
)

// Scenario: an agent fills its memory with notes (issue #589). From 80% of
// the 200 notes each change asks it to merge down to 70%, Coder's nudge; the
// 201st note is refused and nothing is written. A note whose subject appears
// twice is not replaced blindly: the refusal names both lines, as Anthropic's
// tool does. A long note is read in parts: view cuts it at 16,000 characters
// and says where to read on. A directory is not renamed onto a note, and
// MEMORY.md is never a directory.
func TestAMemoryKeepsItsLimitsAndSaysSo(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	ctx := t.Context()
	dir := memorystore.AgentDir("implementer")

	run := func(c memorystore.Command) (string, error) {
		t.Helper()

		return s.Run(ctx, dir, c)
	}

	var out string

	for i := range 160 {
		var err error

		out, err = run(memorystore.Command{
			Command: memorystore.Create, Path: fmt.Sprintf("/memories/n%03d.md", i), FileText: new("x"),
		})
		require.NoError(t, err)
	}

	assert.Contains(t, out, "Notes: 160 of 200 files (80%). Nearly full: merge notes on one subject and delete "+
		"stale ones, until under 70% (140 files).")

	for i := 160; i < memorystore.MaxNotes; i++ {
		_, err := run(memorystore.Command{
			Command: memorystore.Create, Path: fmt.Sprintf("/memories/n%03d.md", i), FileText: new("x"),
		})
		require.NoError(t, err)
	}

	_, err := run(memorystore.Command{Command: memorystore.Create, Path: "/memories/one-more.md", FileText: new("x")})
	require.ErrorContains(t, err, "holds 200 notes, its limit")

	notes, err := s.Notes(ctx, dir)
	require.NoError(t, err)
	assert.Len(t, notes, memorystore.MaxNotes, "a refused note is not written")

	_, err = run(memorystore.Command{Command: memorystore.Delete, Path: "/memories/n000.md"})
	require.NoError(t, err)

	_, err = run(memorystore.Command{
		Command: memorystore.Create, Path: "/memories/team.md", FileText: new("Linh reviews Go.\nMinh\nLinh reviews Go."),
	})
	require.NoError(t, err)

	_, err = run(memorystore.Command{
		Command: memorystore.StrReplace, Path: "/memories/team.md", OldStr: new("Linh reviews Go."), NewStr: new("x"),
	})
	require.ErrorContains(t, err, "Multiple occurrences of old_str `Linh reviews Go.` in lines: 1, 3.")

	_, err = run(memorystore.Command{Command: memorystore.Delete, Path: "/memories/n001.md"})
	require.NoError(t, err)

	long := strings.Repeat("y\n", 4000)

	_, err = run(memorystore.Command{Command: memorystore.Create, Path: "/memories/long.md", FileText: &long})
	require.NoError(t, err)

	out, err = run(memorystore.Command{Command: memorystore.View, Path: "/memories/long.md"})
	require.NoError(t, err)
	assert.LessOrEqual(t, len([]rune(out)), 16100)
	assert.Contains(t, out, "(cut at 16,000 characters: read on with view_range [",
		"many short lines pass 16,000 characters with their numbers")

	out, err = run(memorystore.Command{
		Command: memorystore.View, Path: "/memories/long.md", ViewRange: []int{3999, -1},
	})
	require.NoError(t, err)
	assert.Equal(t, "Here's the content of /memories/long.md with line numbers:\n  3999\ty\n  4000\ty", out)

	_, err = run(memorystore.Command{Command: memorystore.Delete, Path: "/memories/n002.md"})
	require.NoError(t, err)

	_, err = run(memorystore.Command{
		Command: memorystore.Create, Path: "/memories/release/steps.md", FileText: new("Tag from main."),
	})
	require.NoError(t, err)

	_, err = run(memorystore.Command{
		Command: memorystore.Rename, OldPath: "/memories/release", NewPath: "/memories/team.md",
	})
	require.ErrorContains(t, err, "The destination /memories/team.md already exists")

	_, err = run(memorystore.Command{
		Command: memorystore.Create, Path: "/memories/MEMORY.md/x.md", FileText: new("x"),
	})
	require.ErrorContains(t, err, "/memories/MEMORY.md is a file, not a directory")

	_, err = s.WriteNote(ctx, dir, "/memories/USER.md", "x")
	require.ErrorContains(t, err, "is not a note", "the owner edits USER.md as the memory about him")
}
