package prices

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The newest of a family is read from the versions in its ids: 5.5 is newer
// than 5 and 4.8, a dated snapshot is not a newer version, and an id of
// another shape stands for no short name (issue #399).
func TestNewestClaudeReadsTheVersions(t *testing.T) {
	t.Parallel()

	book := map[string]Model{}
	for _, id := range []string{
		"claude-opus-4-8", "claude-opus-5", "claude-opus-5-5", "claude-opus-4-5-20251101",
		"claude-haiku-4-5", "claude-haiku-4-5-20991231", "claude-sonnet-5-5-fast", "claude-3-opus",
	} {
		book[id] = Model{}
	}

	assert.Equal(t, map[string]string{"opus": "claude-opus-5-5", "haiku": "claude-haiku-4-5"}, newestClaude(book))
}
