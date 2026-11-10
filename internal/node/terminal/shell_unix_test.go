//go:build !windows

package terminal_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/terminal"
)

// Scenario: a real shell on a pseudo-terminal, as the node starts it. A
// command typed into it runs and its output comes back, and closing it ends
// the shell so its reader returns.
func TestStartShellRunsWhatIsTyped(t *testing.T) {
	t.Parallel()

	shell, err := terminal.StartShell(24, 80)
	require.NoError(t, err)

	var (
		mu     sync.Mutex
		screen strings.Builder
	)

	done := make(chan struct{})

	go func() {
		defer close(done)

		buf := make([]byte, 4096)

		for {
			n, err := shell.Read(buf)

			mu.Lock()
			screen.Write(buf[:n])
			mu.Unlock()

			if err != nil {
				return
			}
		}
	}()

	require.NoError(t, shell.Resize(40, 120))

	// The marker is computed by the shell, so the echo of the typed line does not satisfy the test.
	_, err = shell.Write([]byte("echo terminal-$((40+2))-works\n"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return strings.Contains(screen.String(), "terminal-42-works")
	}, 10*time.Second, 20*time.Millisecond)

	require.NoError(t, shell.Close())

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shell's output did not end when it was closed")
	}
}
