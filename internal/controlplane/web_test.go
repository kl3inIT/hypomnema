package controlplane

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// chatsWith holds the gateway key of one chat's running pi.
type chatsWith map[string]string

func (c chatsWith) Key(chatID string) (string, bool) {
	k, ok := c[chatID]

	return k, ok
}

// stillClock is a clock that does not move: every row written by it shares one millisecond.
type stillClock struct{}

func (stillClock) Now() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }

// Scenario: a worker's search is recorded on its Dispatch's gateway key,
// a Bot's on the key of its chat's pi, as model web/search with the
// provider that answered; a page read as web/fetch. A worker whose Dispatch
// has no gateway key, and a token of no chat, are not recorded. The calls are
// recorded in one millisecond, as on a fast machine, and still read newest
// first (issue #444).
func TestEachWebCallIsRecordedOnTheKeyOfWhoAsked(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	gateway := modelgateway.New(db, stillClock{}, platform.NewID, gatewayProviders(svc),
		func(context.Context, string) (string, error) { return "", nil })

	exa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text",`+
			`"text":"Title: A\nURL: https://a.example\nText: a"}]}}`)
	}))
	t.Cleanup(exa.Close)

	metered := meteredWeb{
		web: websearch.New(websearch.Config{
			ExaMCPURL: exa.URL, ParallelMCPURL: exa.URL, TavilyURL: exa.URL, FirecrawlURL: exa.URL, YouMCPURL: exa.URL,
			Settings: func(context.Context) (websearch.Settings, error) {
				return websearch.Settings{Off: []string{"parallel", "firecrawl", "tavily", "you"}}, nil
			},
		}),
		gateway: gateway, logger: slog.New(slog.DiscardHandler),
	}

	_, err = gateway.AddDispatchKey(ctx, "dsp_1", "gpt-sol")
	require.NoError(t, err)

	chatKey, chatSecret, err := gateway.AddChatKey(ctx, "chat_1", "")
	require.NoError(t, err)

	worker := workerWeb{metered}
	bot := botWeb{meteredWeb: metered, chats: chatsWith{"chat_1": chatSecret}}

	_, err = worker.Search(ctx, "dsp_1", "q", 0, "")
	require.NoError(t, err)
	_, err = bot.Search(ctx, "chat_1", "q2", 0, "")
	require.NoError(t, err)
	_, err = worker.Search(ctx, "dsp_without_key", "q3", 0, "")
	require.NoError(t, err)
	_, err = bot.Search(ctx, "", "q4", 0, "")
	require.NoError(t, err)
	_, err = bot.Fetch(ctx, "chat_1", "http://169.254.169.254/")
	require.ErrorIs(t, err, websearch.ErrRefused)

	rows, err := gateway.ListUsage(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	dispatchKey, err := gateway.DispatchKey(ctx, "dsp_1")
	require.NoError(t, err)

	fetch, chat, work := rows[0], rows[1], rows[2]
	assert.Equal(t, dispatchKey.ID, work.KeyID)
	assert.Equal(t, modelgateway.ModelWebSearch, work.Model)
	assert.Equal(t, websearch.ProviderExa, work.Provider)
	assert.Equal(t, chatKey.ID, chat.KeyID)
	assert.Equal(t, chatKey.ID, fetch.KeyID)
	assert.Equal(t, modelgateway.ModelWebFetch, fetch.Model)
	assert.Equal(t, modelgateway.OutcomeUpstreamError, fetch.Outcome)
}

// The web search keys are the vault's secrets named as keys (issue #393):
// web-search-exa, the name Exa's one key had, and web-search-exa-2 are Exa's,
// web-search-tavily is Tavily's; another secret is no key. No vault is no key.
func TestTheWebSearchKeysAreReadFromTheVault(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "hypomnema.db")
	cfg := vaultConfig(t, path, "k1", keygen(t, "k1"))
	require.NoError(t, Migrate(ctx, cfg, io.Discard))

	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, nil)
	logger := slog.New(slog.DiscardHandler)

	keys, err := searchKeys(secrets, logger)(ctx)
	require.NoError(t, err)
	assert.Empty(t, keys, "no such secret, no key")

	for name, value := range map[string]string{
		"web-search-exa": "exa-1", "web-search-exa-2": "exa-2", "web-search-tavily": "tvly", "github-token": "x",
	} {
		_, err = secrets.Create(ctx, vault.NewSecret{
			Name: name, Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: value},
		})
		require.NoError(t, err)
	}

	keys, err = searchKeys(secrets, logger)(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []websearch.Key{
		{Provider: "exa", Name: "web-search-exa", Value: "exa-1"},
		{Provider: "exa", Name: "web-search-exa-2", Value: "exa-2"},
		{Provider: "tavily", Name: "web-search-tavily", Value: "tvly"},
	}, keys)

	keys, err = searchKeys(vault.New(db, platform.SystemClock{}, platform.NewID, nil, nil), logger)(ctx)
	require.NoError(t, err)
	assert.Empty(t, keys, "no vault, no key")
}
