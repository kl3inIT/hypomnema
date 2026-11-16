package routines

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Which failures pass by themselves, after OpenClaw's retry-hint tests: a
// status is one with its words, never a bare number in a sentence.
func TestWhichFailuresPass(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{
		"pi's model call failed: 429 too many requests": true,
		"rate limit exceeded":                           true,
		"HTTP 429":                                      true,
		"overloaded_error":                              true,
		"529 try again":                                 true,
		"fetch failed: ECONNRESET":                      true,
		"the request timed out":                         true,
		"the script was stopped at its limit of 10m0s (timeout)": true,
		"502 Bad Gateway":               true,
		"status code 503":               true,
		"503":                           true,
		"the script exited with code 1": false,
		"exited with 503 lines":         false,
		"context limit 512 exceeded":    false,
		"pid 429 killed":                false,
		"the owner rejected it":         false,
		"invalid api key":               false,
		"":                              false,
	} {
		assert.Equal(t, want, passes(text), "%q", text)
	}
}

// A prompt Routine's script wakes its Bot unless it printed nothing or its
// last line that is not empty is {"wakeAgent": false}, after Hermes's
// _parse_wake_gate: anything else, JSON or not, wakes it (issue #656).
func TestWhatWakesTheBot(t *testing.T) {
	t.Parallel()

	for output, want := range map[string]bool{
		"":                                     false,
		" \n\n":                                false,
		`{"wakeAgent": false}`:                 false,
		"14 charts\n{\"wakeAgent\":false}\n\n": false,
		`{"wakeAgent": false, "seen": 14}`:     false,
		`{"wakeAgent": true}`:                  true,
		"chart 164 changed\n":                  true,
		"{\"wakeAgent\": false}\nchart 164 changed\n": true,
		`{"changed": [164]}`:                          true,
		`{"wakeAgent": "false"}`:                      true,
		`[false]`:                                     true,
	} {
		assert.Equal(t, want, wakes(output), "%q", output)
	}
}

// The launcher is stopped by closing its input, not by a signal: a command
// that reads its input to the end and then exits is ended by stopping it,
// with its own exit code; one that ends by itself gives its output and code.
// The run's token is the first line of its input.
func TestACommandIsStoppedByClosingItsInput(t *testing.T) {
	t.Parallel()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}

	out, code, err := run(t.Context(), []string{sh, "-c", "echo 3 rows written; echo warned >&2; exit 3"}, "")
	require.NoError(t, err)
	assert.Equal(t, 3, code)
	assert.Contains(t, out, "3 rows written")
	assert.Contains(t, out, "warned", "what it printed as errors is kept with the rest")

	out, _, err = run(t.Context(), []string{sh, "-c", "IFS= read -r token; echo \"token=$token\""}, "hyp_bot_v1_run")
	require.NoError(t, err)
	assert.Equal(t, "token=hyp_bot_v1_run\n", out)

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan int, 1)

	go func() {
		_, code, _ := run(ctx, []string{sh, "-c", "cat > /dev/null; exit 7"}, "")
		done <- code
	}()

	select {
	case <-done:
		require.Fail(t, "the command ended while its input was open")
	case <-time.After(200 * time.Millisecond):
	}

	stop()

	select {
	case code := <-done:
		assert.Equal(t, 7, code)
	case <-time.After(10 * time.Second):
		require.Fail(t, "closing its input did not end the command")
	}

	_, _, err = run(t.Context(), []string{"/no/such/launcher"}, "hyp_bot_v1_run")
	require.Error(t, err, "a command that cannot be started is an error, not an exit code")
}
