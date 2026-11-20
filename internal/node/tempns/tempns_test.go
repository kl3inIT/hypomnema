package tempns

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: with a bubblewrap that works, a worker's argv runs inside it with
// only /tmp and /var/tmp bound to the Dispatch's temp directory; without one,
// or without a temp directory, the argv is left as it is.
func TestWrapBindsOnlyTheTempDirectories(t *testing.T) {
	t.Parallel()

	on := Isolation{Bwrap: "/usr/bin/bwrap"}
	assert.Equal(t, []string{
		"/usr/bin/bwrap", "--dev-bind", "/", "/", "--bind", "/n/run/d/tmp", "/tmp", "--bind", "/n/run/d/tmp", "/var/tmp",
		"--", "claude", "-p",
	}, on.Wrap([]string{"claude", "-p"}, "/n/run/d/tmp"))
	assert.Equal(t, []string{"claude"}, on.Wrap([]string{"claude"}, ""))
	assert.Equal(t, []string{"claude"}, Isolation{Reason: "none"}.Wrap([]string{"claude"}, "/n/run/d/tmp"))
}

// Scenario: the node says at hello whether the isolation is on, and why not.
func TestHelloSaysWhyTheIsolationIsOff(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &nodeproto.TempIsolation{On: true}, Isolation{Bwrap: "/usr/bin/bwrap"}.Hello())
	assert.Equal(t, &nodeproto.TempIsolation{Reason: "no bwrap"}, Isolation{Reason: "no bwrap"}.Hello())
}
