package controlplane_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: the whole path the owner sets up for pi. A provider's key goes
// into the vault, the provider names it, a model is declared and a gateway
// key made; the running control plane then serves a chat request, calling the
// provider with the key from the vault, and the owner sees the request in the
// gateway's usage.
func TestTheControlPlaneServesAChatRequestWithAKeyFromTheVault(t *testing.T) {
	t.Parallel()

	var authorization atomic.Value

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization.Store(r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello"}}],`+
			`"usage":{"prompt_tokens":9,"completion_tokens":3}}`)
	}))
	t.Cleanup(upstream.Close)

	run := func(cfg controlplane.Config, stdin string, args ...string) string {
		var out bytes.Buffer

		require.NoError(t, controlplane.Command(t.Context(), cfg, args, strings.NewReader(stdin), &out), args)

		return out.String()
	}

	rootKey, _, _ := strings.Cut(run(controlplane.Config{}, "", "secrets", "keygen", "k1"), "\n")
	addr := freeAddr(t)

	cfg, err := controlplane.LoadConfig(func(name string) string {
		return map[string]string{
			"HYPOMNEMA_DATABASE_PATH":       filepath.Join(t.TempDir(), "hypomnema.db"),
			"HYPOMNEMA_HTTP_ADDR":           addr,
			"HYPOMNEMA_VAULT_KEYS":          rootKey,
			"HYPOMNEMA_VAULT_ACTIVE_KEY_ID": "k1",
		}[name]
	})
	require.NoError(t, err)
	require.NoError(t, controlplane.Migrate(t.Context(), cfg, io.Discard))

	run(cfg, "sk-the-providers-key\n", "secrets", "add", "router", "--from-file", "-")

	db, err := sqlite.Open(t.Context(), cfg.Database)
	require.NoError(t, err)

	_, err = orchestration.New(db, platform.SystemClock{}, platform.NewID).ApplyProvider(t.Context(), "",
		"name: router\nkind: openai_compatible\nbase_url: "+upstream.URL+"/v1\n"+
			"api_key_ref: vault:router\nmodels: [cx/gpt-6.1-sol]\n")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	model := filepath.Join(t.TempDir(), "gpt-sol.yaml")
	require.NoError(t, os.WriteFile(model,
		[]byte("name: gpt-sol\ntargets: [{provider: router, model: cx/gpt-6.1-sol}]\n"), 0o600))
	run(cfg, "", "gateway", "models", "apply", model)

	gatewayKey := regexp.MustCompile(`(?m)^hyp_gateway_v1_\S+$`).FindString(run(cfg, "", "gateway", "keys", "add", "pi"))
	require.NotEmpty(t, gatewayKey)

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)

	go func() { served <- controlplane.Serve(ctx, cfg, io.Discard) }()

	var answer string

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://"+addr+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-sol","messages":[]}`))
		require.NoError(c, err)
		req.Header.Set("Authorization", "Bearer "+gatewayKey)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(c, err)

		defer resp.Body.Close() //nolint:errcheck // a test read

		body, err := io.ReadAll(resp.Body)
		require.NoError(c, err)
		require.Equal(c, http.StatusOK, resp.StatusCode, string(body))

		answer = string(body)
	}, 10*time.Second, 50*time.Millisecond)

	assert.Contains(t, answer, `"content":"hello"`)
	assert.Equal(t, "Bearer sk-the-providers-key", authorization.Load())

	cancel()
	require.NoError(t, <-served)

	usage := run(cfg, "", "gateway", "usage")
	assert.Contains(t, usage, "\tpi\tgpt-sol\trouter:cx/gpt-6.1-sol\tok 200\tin 9 (cached 0) out 3 (reasoning 0)\t")
	assert.NotContains(t, usage, "sk-the-providers-key")
}
