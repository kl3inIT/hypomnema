package clientcli

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// world is a control plane serving the client API, and a home for the CLI.
type world struct {
	svc  *orchestration.Service
	url  string
	home string
}

func newWorld(t *testing.T) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, slog.New(slog.DiscardHandler), stop).Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	return &world{svc: svc, url: hs.URL, home: filepath.Join(t.TempDir(), "home")}
}

// login makes a token of kind and logs the CLI in with it.
func (w *world) login(t *testing.T, kind bearertoken.Kind) string {
	t.Helper()

	_, secret, err := w.svc.AddToken(t.Context(), "cli-"+string(kind), kind)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte(secret+"\n"), 0o600))

	out, err := w.cmd(t, "login", "--url", w.url+"/", "--token-file", file)
	require.NoError(t, err)

	return out
}

func (w *world) cmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	err := Command(t.Context(), w.home, args, &out)

	return out.String(), err
}

// Scenario: the owner logs in from the laptop; the token is checked and kept
// in a private file. A token the control plane does not know is refused and
// nothing is saved.
func TestLoginChecksAndKeepsTheToken(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	unknown, err := bearertoken.Generate(bearertoken.Owner)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte(unknown), 0o600))

	_, err = w.cmd(t, "login", "--url", w.url, "--token-file", file)
	require.ErrorContains(t, err, "unauthorized")

	_, err = LoadConfig(w.home)
	require.Error(t, err, "a refused login saves nothing")

	out := w.login(t, bearertoken.Owner)
	assert.Equal(t, "logged in to "+w.url+" as cli-owner (owner)\n", out)

	cfg, err := LoadConfig(w.home)
	require.NoError(t, err)
	assert.Equal(t, w.url, cfg.URL, "the trailing slash is dropped")

	_, err = w.cmd(t, "login", "--url", "ftp://x", "--token-file", file)
	require.Error(t, err)

	_, err = w.cmd(t, "questions")
	require.NoError(t, err)

	err = Command(t.Context(), filepath.Join(t.TempDir(), "nobody"), []string{"questions"}, &bytes.Buffer{})
	require.ErrorContains(t, err, "hypomnema login")
}

// Scenario: the owner starts a Run from the laptop and follows it. Events
// are printed as they come; when the Task ends the Run is finished.
func TestRunFollowsUntilTheTaskEnds(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "run", "--repo", "kl3inIT/second-bite", "--detach", "add", "Google", "login")
	require.NoError(t, err)
	assert.Regexp(t, `^run run_\w+, task tsk_\w+\n$`, out)

	_, err = w.cmd(t, "run", "--detach", "no repository")
	require.ErrorIs(t, err, ErrUsage)

	_, err = w.cmd(t, "run", "--repo", "not a repo", "--detach", "x")
	require.ErrorContains(t, err, "validation_failed")

	cfg, err := LoadConfig(w.home)
	require.NoError(t, err)

	api, err := newClient(cfg)
	require.NoError(t, err)

	var buf syncBuffer

	o := &owner{api: api, out: &buf}
	done := make(chan error, 1)

	go func() {
		done <- o.run(context.WithoutCancel(t.Context()), []string{"--repo", "kl3inIT/demo", "follow me"})
	}()

	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "task.created") }, 5*time.Second,
		10*time.Millisecond)

	taskID := strings.Fields(strings.SplitN(buf.String(), "task ", 2)[1])[0]
	require.NoError(t, w.svc.CancelTask(t.Context(), taskID))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop following when its Task ended")
	}

	assert.Contains(t, buf.String(), "task canceled")

	task, err := w.svc.GetTask(t.Context(), taskID)
	require.NoError(t, err)

	run, err := w.svc.GetRun(t.Context(), task.RunID)
	require.NoError(t, err)
	assert.Equal(t, orchestration.RunFinished, run.Status)
	assert.Equal(t, "cli-owner", run.CreatedBy)
}

// Scenario: the owner answers by number, by an option's label in any case,
// or with free text.
func TestParseAnswer(t *testing.T) {
	t.Parallel()

	options := []openapi.Option{{Label: "red"}, {Label: "Green"}}
	option := func(i int) *int { return &i }
	text := func(s string) *string { return &s }

	for _, c := range []struct {
		typed string
		want  openapi.Answer
	}{
		{"2", openapi.Answer{Option: option(1)}},
		{"green", openapi.Answer{Option: option(1)}},
		{" RED ", openapi.Answer{Option: option(0)}},
		{"blue please", openapi.Answer{Text: text("blue please")}},
	} {
		assert.Equal(t, c.want, ParseAnswer(options, c.typed), c.typed)
	}

	assert.Equal(t, openapi.Answer{Text: text("green")}, ParseAnswer(nil, "green"))
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
