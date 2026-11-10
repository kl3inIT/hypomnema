package controlplane

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// codexIssuer stands in for auth.openai.com for one login that the owner
// completes at once.
func codexIssuer(t *testing.T) string {
	t.Helper()

	claims := base64.RawURLEncoding.EncodeToString([]byte(
		`{"email":"owner@example.com","https://api.openai.com/auth":` +
			`{"chatgpt_account_id":"acct-chatgpt","chatgpt_plan_type":"plus"}}`))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/accounts/deviceauth/usercode", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"device_auth_id":"dev-1","user_code":"ABCD-1234","interval":5}`)
	})
	mux.HandleFunc("POST /api/accounts/deviceauth/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"authorization_code":"the-code","code_verifier":"v","code_challenge":"c"}`)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"access-never-printed","refresh_token":"refresh-never-printed",`+
			`"id_token":"e30.`+claims+`.sig","expires_in":3600}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL
}

// Scenario: the owner logs a Codex subscription in on the server. The command
// prints a link and a code and never a token; the account is listed as active
// with its plan; its secret cannot be deleted from under it; removing the
// account removes its tokens too.
func TestOwnerLogsACodexSubscriptionInAndRemovesIt(t *testing.T) {
	t.Parallel()

	cfg := vaultConfig(t, filepath.Join(t.TempDir(), "hypomnema.db"), "k1", keygen(t, "k1"))
	cfg.CodexIssuer = codexIssuer(t)
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	var printed strings.Builder

	run := func(args ...string) (string, error) {
		var out bytes.Buffer

		err := Command(t.Context(), cfg, args, strings.NewReader(""), &out)
		printed.WriteString(out.String())

		return out.String(), err
	}

	out, err := run("accounts", "login", "codex")
	require.NoError(t, err)
	assert.Contains(t, out, cfg.CodexIssuer+"/codex/device")
	assert.Contains(t, out, "ABCD-1234")
	assert.Contains(t, out, "(codex owner@example.com), plan plus; token until ")

	id := regexp.MustCompile(`account (acc_\w+) `).FindStringSubmatch(out)
	require.Len(t, id, 2, out)

	out, err = run("accounts", "list")
	require.NoError(t, err)
	assert.Contains(t, out, id[1]+"\tcodex\towner@example.com\tactive\t\tactive\tplan plus\ttoken until ")

	out, err = run("secrets", "list")
	require.NoError(t, err)
	secret := strings.ReplaceAll("account-"+id[1], "_", "-")
	assert.Contains(t, out, secret+"\toauth_token_set\t")

	_, err = run("secrets", "rm", secret)
	require.ErrorIs(t, err, vault.ErrInUse)
	require.ErrorContains(t, err, "account codex/owner@example.com")

	out, err = run("accounts", "refresh", id[1])
	require.NoError(t, err)
	assert.Contains(t, out, "refreshed; token until ")

	_, err = run("accounts", "rm", id[1])
	require.NoError(t, err)

	out, err = run("accounts", "list")
	require.NoError(t, err)
	assert.Empty(t, out)

	out, err = run("secrets", "list")
	require.NoError(t, err)
	assert.Empty(t, out)

	assert.NotContains(t, printed.String(), "never-printed")

	_, err = run("accounts", "login", "antigravity")
	require.ErrorIs(t, err, subscription.ErrUnknownVendor)

	_, err = run("accounts", "login")
	require.ErrorIs(t, err, ErrUsage)
}

// Scenario: the vault has no root key yet. A login is refused before the
// owner is sent to a browser, since its tokens would have nowhere to go.
func TestALoginNeedsTheVault(t *testing.T) {
	t.Parallel()

	cfg := migratedConfig(t)
	cfg.CodexIssuer = codexIssuer(t)

	out, err := command(t, cfg, "accounts", "login", "codex")
	require.ErrorIs(t, err, vault.ErrNotConfigured)
	assert.Empty(t, out)
}
