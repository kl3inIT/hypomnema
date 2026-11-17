package muse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

func openTestStore(t *testing.T) *store {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "muse.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	s, err := openStore(t.Context(), db)
	require.NoError(t, err)

	return s
}

// Each question is kept with its answer, links, outcome and files, and found
// again by thread and by full text.
func TestQuestionsAreKeptAndSearched(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	_, err := s.keep(ctx, Asked{Account: "acc_1", Thread: aThread, Agent: "worker dsp_1", Prompt: "Who makes rice cookers?",
		Answer: "Several makers [1]", Links: []Link{{URL: "https://example.com"}}, Outcome: OutcomeAnswered,
		Files: []File{{URL: "https://muse.ai/files/x.png", Path: "/m/x.png", Size: 3}}, At: at, Finished: at})
	require.NoError(t, err)

	_, err = s.keep(ctx, Asked{Account: "acc_1", Thread: aThread, Prompt: "And in Vietnam?", Outcome: OutcomeInterrupted,
		Error: "interrupted", At: at.Add(time.Minute), Finished: at.Add(time.Minute)})
	require.NoError(t, err)

	_, err = s.keep(ctx, Asked{Account: "acc_2", Prompt: "lost", Outcome: OutcomeUnknown, At: at, Finished: at})
	require.NoError(t, err)

	chats, err := s.chats(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, chats, 1, "a question with no thread is no chat")
	assert.Equal(t, Chat{Thread: aThread, Account: "acc_1", Asks: 2, Last: "And in Vietnam?", At: at.Add(time.Minute)}, chats[0])

	found, err := s.chats(ctx, `rice "cookers`, 0)
	require.NoError(t, err)
	assert.Len(t, found, 1, "an agent's words are terms, never FTS5 syntax")

	messages, err := s.messages(ctx, aThread, "", 0)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "Who makes rice cookers?", messages[0].Prompt)
	assert.Equal(t, []Link{{URL: "https://example.com"}}, messages[0].Links)
	assert.Equal(t, []File{{URL: "https://muse.ai/files/x.png", Path: "/m/x.png", Size: 3}}, messages[0].Files)
	assert.Equal(t, OutcomeInterrupted, messages[1].Outcome)

	none, err := s.messages(ctx, "", "dishwasher", 0)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// A thread keeps its first account for good.
func TestAThreadKeepsItsAccount(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := t.Context()

	require.NoError(t, s.keepThread(ctx, aThread, "acc_1", time.Now()))
	require.NoError(t, s.keepThread(ctx, aThread, "acc_2", time.Now()))

	account, err := s.threadAccount(ctx, aThread)
	require.NoError(t, err)
	assert.Equal(t, "acc_1", account)

	account, err = s.threadAccount(ctx, "other")
	require.NoError(t, err)
	assert.Empty(t, account)
}

// An account's state, rest and usage are kept apart and read together.
func TestAnAccountsStateIsKept(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	require.NoError(t, s.setState(ctx, "acc_1", StateSignedIn, "", "v1", now))
	require.NoError(t, s.rest(ctx, "acc_1", true, "out of quota"))
	require.NoError(t, s.setUsage(ctx, "acc_1", Usage{UsedPercent: 100, Reset: "Sep 30", Found: true}, now))
	require.NoError(t, s.setState(ctx, "acc_1", StateSignedIn, "", "", now))

	st, err := s.state(ctx, "acc_1")
	require.NoError(t, err)
	assert.Equal(t, StateSignedIn, st.State)
	assert.Equal(t, "v1", st.Viewer, "a check without the viewer keeps it")
	assert.True(t, st.Resting)
	assert.Equal(t, "100% used · resets Sep 30", st.Usage.Text())
	assert.Equal(t, now, st.UsageReadAt)

	on, err := s.toolsOn(ctx)
	require.NoError(t, err)
	assert.True(t, on, "the tools are on until switched off")
	require.NoError(t, s.setToolsOn(ctx, false))
	on, err = s.toolsOn(ctx)
	require.NoError(t, err)
	assert.False(t, on)
}

// A file is fetched with muse.ai's cookies sent to muse.ai only, kept
// private, and a file over its cap or a failed download is said once.
func TestFilesAreKeptWithinTheirCaps(t *testing.T) {
	t.Parallel()

	var cookie string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")

		switch r.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		case "/big.png":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", "30000000")
			_, _ = w.Write(make([]byte, 1024))
		case "/big.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(make([]byte, maxImageBytes+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := openTestStore(t)
	m := &media{dir: t.TempDir(), http: srv.Client(), store: s}
	jar := []browser.Cookie{{Name: "hatch_sess", Value: "secret"}}

	files := m.keepAll(t.Context(), "acc_1", aThread,
		[]string{srv.URL + "/a.png", srv.URL + "/big.png", srv.URL + "/big.pdf", srv.URL + "/gone", "ftp://x"}, jar, nil)
	require.Len(t, files, 5)

	assert.Empty(t, cookie, "the account's cookies go to muse.ai only")
	assert.Equal(t, "image/png", files[0].MIME)
	info, err := os.Stat(files[0].Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Contains(t, files[0].Path, filepath.Join("acc_1", aThread))
	assert.Equal(t, "over 20 MiB, not kept", files[1].Error)
	assert.Empty(t, files[2].Error, "another file has 50 MiB")
	assert.Contains(t, files[3].Error, "404")
	assert.NotEmpty(t, files[4].Error)

	_, err = s.keep(t.Context(), Asked{Account: "acc_1", Thread: aThread, Prompt: "p", Outcome: OutcomeAnswered,
		Files: files, At: time.Now(), Finished: time.Now()})
	require.NoError(t, err)

	again := m.keepAll(t.Context(), "acc_1", aThread, []string{srv.URL + "/big.png"}, jar, nil)
	assert.Empty(t, again[0].Error, "a file not kept says why once")
}

// A blob: address is read in the page up to 10 MiB.
func TestABlobIsReadInThePageWithinItsCap(t *testing.T) {
	t.Parallel()

	page := &fakePage{answer: func(script string) (any, error) {
		if strings.Contains(script, "blob:big") {
			return map[string]any{"ok": false, "size": maxBlobBytes + 1, "error": "too large"}, nil
		}

		return map[string]any{"ok": true, "mime": "image/png", "size": 3, "data": "cG5n"}, nil
	}}

	m := &media{dir: t.TempDir(), http: http.DefaultClient, store: openTestStore(t)}
	files := m.keepAll(context.Background(), "acc_1", "", []string{"blob:https://muse.ai/1", "blob:big"}, nil,
		newDriver(page))

	data, err := os.ReadFile(files[0].Path)
	require.NoError(t, err)
	assert.Equal(t, "png", string(data))
	assert.Contains(t, files[0].Path, "no-thread")
	assert.Equal(t, "over 10 MiB, not kept", files[1].Error)
}
