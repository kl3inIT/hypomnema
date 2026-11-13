package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/shared/release"
)

// A release is newer than an older release, and than a build of a commit only from the next version on: the control
// plane on zm runs builds of commits (v0.9.4-94-g1a4fce6), which come after v0.9.4 and before v0.9.5.
func TestNewerOrdersReleasesAndBuildsOfCommits(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"v0.9.5", "v0.9.4", true},
		{"v0.10.0", "v0.9.12", true},
		{"v0.9.4", "v0.9.4", false},
		{"v0.9.4", "v0.9.5", false},
		{"v0.9.5", "v0.9.4-94-g1a4fce6", true},
		{"v0.9.4", "v0.9.4-94-g1a4fce6", false},
		{"v0.9.5", "dev", false},
		{"v0.9.5-1-gabc", "v0.9.4", false},
	} {
		assert.Equal(t, c.newer, release.Newer(c.a, c.b), "%s newer than %s", c.a, c.b)
	}
}
