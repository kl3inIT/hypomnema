package nodeexec

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Each sudo the shell runs as a command asks its helper (-A), wherever it is
// in the command; a word "sudo" that is not a command is left alone, and so
// is a command the parser cannot read (Hermes rewrites the same words).
func TestEverySudoTheShellRunsAsksItsHelper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		want    string
		found   bool
	}{
		{"df -h", "df -h", false},
		{"sudo apt-get update", "sudo -A apt-get update", true},
		{
			"sudo apt-get update && sudo apt-get install -y jq",
			"sudo -A apt-get update && sudo -A apt-get install -y jq", true,
		},
		{"echo hi | sudo tee /etc/x", "echo hi | sudo -A tee /etc/x", true},
		{"for f in a b; do sudo rm $f; done", "for f in a b; do sudo -A rm $f; done", true},
		{"cd /srv && (sudo du -sh .)", "cd /srv && (sudo -A du -sh .)", true},
		{"X=1 sudo -u dat id", "X=1 sudo -A -u dat id", true},
		{"echo sudo is here; grep sudo /etc/group", "echo sudo is here; grep sudo /etc/group", false},
		{"'sudo' id", "'sudo' id", false},
		{"sudo id; echo 'unclosed", "sudo id; echo 'unclosed", false},
	}

	for _, tt := range tests {
		got, found := withAskpass(tt.command)
		assert.Equal(t, tt.want, got, tt.command)
		assert.Equal(t, tt.found, found, tt.command)
	}
}

// A long output keeps its first and last half and says how much of its
// middle was left out, however it was written.
func TestALongOutputKeepsItsStartAndItsEnd(t *testing.T) {
	t.Parallel()

	b := newHeadTail(8)
	for _, part := range []string{"ab", "cdef", "ghijk", "l", "mnop"} {
		_, _ = b.Write([]byte(part))
	}

	got, cut := b.String()
	assert.Equal(t, "abcd\n[… 8 bytes left out …]\nmnop", got)
	assert.Equal(t, int64(8), cut)

	short := newHeadTail(8)
	_, _ = short.Write([]byte("abcdef"))

	got, cut = short.String()
	assert.Equal(t, "abcdef", got)
	assert.Zero(t, cut)
}
