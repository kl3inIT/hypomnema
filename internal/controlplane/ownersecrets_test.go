package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// vaultAPI is the client API over a real vault, with the owner's token and a
// coordinator's.
type vaultAPI struct {
	t           *testing.T
	url         string
	owner       string
	ownerID     string
	coordinator string
	svc         *orchestration.Service
	tokens      connections
	cfg         Config
}

func newVaultAPI(t *testing.T) *vaultAPI {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	cfg := vaultConfig(t, path, "k1", keygen(t, "k1"))
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	referrers := secretReferrers(svc)
	tokens := connections{
		svc: svc, vault: vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, referrers),
	}

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, slog.New(slog.DiscardHandler), stop).WithHostTokens(tokens).
		WithSecrets(ownerSecrets{vault: tokens.vault, referrers: referrers}).Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	owner, ownerSecret, err := svc.AddToken(t.Context(), "owner", bearertoken.Owner)
	require.NoError(t, err)

	_, coordSecret, err := svc.AddToken(t.Context(), "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	return &vaultAPI{
		t: t, url: hs.URL + clientapi.BasePath, owner: ownerSecret, ownerID: owner.ID, coordinator: coordSecret,
		svc: svc, tokens: tokens, cfg: cfg,
	}
}

// call sends a request as a token and returns the status, the body decoded
// when it is JSON, the raw body and the Cache-Control header.
func (a *vaultAPI) call(token, method, path string, body any) (int, map[string]any, string, string) {
	a.t.Helper()

	var in io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(a.t, err)

		in = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(a.t.Context(), method, a.url+path, in)
	require.NoError(a.t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(a.t, err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(a.t, err)

	var out map[string]any
	_ = json.Unmarshal(raw, &out)

	return resp.StatusCode, out, string(raw), resp.Header.Get("Cache-Control")
}

// names lists the vault as the owner sees it: each secret's name with what
// uses it.
func (a *vaultAPI) names() map[string][]string {
	a.t.Helper()

	status, _, raw, _ := a.call(a.owner, http.MethodGet, "/secrets", nil)
	require.Equal(a.t, http.StatusOK, status)

	var list struct {
		Data []struct {
			Name   string   `json:"name"`
			UsedBy []string `json:"used_by"`
		} `json:"data"`
	}
	require.NoError(a.t, json.Unmarshal([]byte(raw), &list))

	out := map[string][]string{}
	for _, s := range list.Data {
		out[s.Name] = s.UsedBy
	}

	return out
}

// Scenario: the owner keeps an API key in the vault, looks at it, replaces
// it and removes it. The list never carries a value; the value comes one
// secret at a time, is not to be stored, and each read is recorded with the
// secret's name and the token, never the value.
func TestTheOwnerReadsASecretsValue(t *testing.T) {
	t.Parallel()

	a := newVaultAPI(t)

	status, body, _, _ := a.call(a.owner, http.MethodPut, "/secrets/openai-key", map[string]string{"value": routerKey})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "generic", body["kind"])
	assert.EqualValues(t, 1, body["version"])

	_, _, raw, _ := a.call(a.owner, http.MethodGet, "/secrets", nil)
	assert.Contains(t, raw, "openai-key")
	assert.NotContains(t, raw, routerKey, "a list never carries a value")

	events, err := a.svc.LastEventSeq(t.Context())
	require.NoError(t, err)

	status, body, _, cache := a.call(a.owner, http.MethodGet, "/secrets/openai-key/value", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, map[string]any{"value": routerKey}, body["values"])
	assert.Equal(t, "no-store", cache)

	reads, err := a.svc.SecretReads(t.Context(), "openai-key", 10)
	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, [2]string{"openai-key", a.ownerID}, [2]string{reads[0].Secret, reads[0].TokenID})

	after, err := a.svc.LastEventSeq(t.Context())
	require.NoError(t, err)
	assert.Equal(t, events, after, "a read is not in the event log, which every token reads")

	status, body, _, _ = a.call(a.owner, http.MethodPut, "/secrets/openai-key", map[string]string{"value": "sk-second"})
	require.Equal(t, http.StatusOK, status)
	assert.EqualValues(t, 2, body["version"], "a new value is a new version")

	_, body, _, _ = a.call(a.owner, http.MethodGet, "/secrets/openai-key/value", nil)
	assert.Equal(t, map[string]any{"value": "sk-second"}, body["values"])

	status, _, _, _ = a.call(a.owner, http.MethodGet, "/secrets/missing/value", nil)
	assert.Equal(t, http.StatusNotFound, status)

	status, _, raw, _ = a.call(a.owner, http.MethodPut, "/secrets/Bad_Name", map[string]string{"value": "x"})
	assert.GreaterOrEqual(t, status, 400, raw)

	status, _, _, _ = a.call(a.owner, http.MethodDelete, "/secrets/openai-key", nil)
	require.Equal(t, http.StatusNoContent, status)
	assert.NotContains(t, a.names(), "openai-key")

	// The command on the control plane host prints the same value.
	_, _, _, _ = a.call(a.owner, http.MethodPut, "/secrets/note", map[string]string{"value": "written-down"})

	var out bytes.Buffer
	require.NoError(t, Command(t.Context(), a.cfg, []string{"secrets", "show", "note"}, strings.NewReader(""), &out))
	assert.Equal(t, "written-down\n", out.String())

	reads, err = a.svc.SecretReads(t.Context(), "note", 10)
	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, "host", reads[0].TokenID, "a read on the host is recorded too")
}

// Scenario: pi's token asks for the vault. Every operation on a secret is
// refused, and what it was refused never appears in the answer.
func TestACoordinatorNeverReadsASecret(t *testing.T) {
	t.Parallel()

	a := newVaultAPI(t)

	status, _, _, _ := a.call(a.owner, http.MethodPut, "/secrets/openai-key", map[string]string{"value": routerKey})
	require.Equal(t, http.StatusOK, status)

	for _, c := range [][2]string{
		{http.MethodGet, "/secrets"},
		{http.MethodGet, "/secrets/openai-key/value"},
		{http.MethodDelete, "/secrets/openai-key"},
	} {
		status, _, raw, _ := a.call(a.coordinator, c[0], c[1], nil)
		assert.Equal(t, http.StatusForbidden, status, c)
		assert.NotContains(t, raw, routerKey, c)
	}

	status, _, raw, _ := a.call(a.coordinator, http.MethodPut, "/secrets/openai-key", map[string]string{"value": "x"})
	assert.Equal(t, http.StatusForbidden, status)
	assert.NotContains(t, raw, routerKey)

	reads, err := a.svc.SecretReads(t.Context(), "openai-key", 10)
	require.NoError(t, err)
	assert.Empty(t, reads, "a refused read is not a read")

	_, body, _, _ := a.call(a.owner, http.MethodGet, "/secrets/openai-key/value", nil)
	assert.Equal(t, map[string]any{"value": routerKey}, body["values"], "and the secret is as it was")
}

// Scenario: the owner connects a GitHub account. Its token is a secret the
// vault page shows with what uses it; it cannot be removed while the
// Connection is there, and replacing its value there is what the next
// Dispatch gets.
func TestAConnectionsSecretIsShownWithWhatUsesIt(t *testing.T) {
	t.Parallel()

	a := newVaultAPI(t)

	require.NoError(t, a.tokens.Connect(t.Context(), githost.GitHubHost, "work", "ghp_work_token"))

	const name = "conn-github-com-work"

	assert.Equal(t, []string{"connection github.com:work"}, a.names()[name])

	_, body, _, _ := a.call(a.owner, http.MethodGet, "/secrets/"+name+"/value", nil)
	assert.Equal(t, map[string]any{"value": "ghp_work_token"}, body["values"])

	status, _, raw, _ := a.call(a.owner, http.MethodDelete, "/secrets/"+name, nil)
	assert.Equal(t, http.StatusConflict, status, "a secret in use is not removed")
	assert.Contains(t, raw, "connection github.com:work")

	status, _, _, _ = a.call(a.owner, http.MethodPut, "/secrets/"+name, map[string]string{"value": "ghp_replaced"})
	require.Equal(t, http.StatusOK, status)

	got, ok := a.tokens.Token(t.Context(), githost.GitHubHost, "acme/shop")
	require.True(t, ok)
	assert.Equal(t, "ghp_replaced", got, "what uses the secret gets the new value")

	require.NoError(t, a.tokens.Disconnect(t.Context(), githost.GitHubHost, "work"))
	assert.NotContains(t, a.names(), name, "disconnecting removes the secret")
}

// Scenario: a token set is read with each of its values, and replaced with
// its complete set; a set without its refresh token is refused.
func TestATokenSetIsReadAndReplacedAsASet(t *testing.T) {
	t.Parallel()

	a := newVaultAPI(t)

	var out bytes.Buffer
	require.NoError(t, Command(t.Context(), a.cfg,
		[]string{"secrets", "add", "google-work", "--kind", "oauth_token_set", "--from-file", "-"},
		strings.NewReader(`{"refresh_token":"rt-1","access_token":"at-1"}`), &out))

	status, body, _, _ := a.call(a.owner, http.MethodGet, "/secrets/google-work/value", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, map[string]any{"refresh_token": "rt-1", "access_token": "at-1"}, body["values"])

	status, _, raw, _ := a.call(a.owner, http.MethodPut, "/secrets/google-work", map[string]string{"value": "typed"})
	assert.Equal(t, http.StatusBadRequest, status, "a token set needs its refresh token: %s", raw)
	assert.NotContains(t, raw, "rt-1")

	status, body, _, _ = a.call(a.owner, http.MethodPut, "/secrets/google-work",
		map[string]any{"values": map[string]string{"refresh_token": "rt-2"}})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "oauth_token_set", body["kind"])
	assert.EqualValues(t, 2, body["version"])

	_, body, _, _ = a.call(a.owner, http.MethodGet, "/secrets/google-work/value", nil)
	assert.Equal(t, map[string]any{"refresh_token": "rt-2"}, body["values"])

	status, _, _, _ = a.call(a.owner, http.MethodPut, "/secrets/empty", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, status, "a secret needs a value")

	out.Reset()
	require.NoError(t, Command(t.Context(), a.cfg, []string{"secrets", "show", "google-work"},
		strings.NewReader(""), &out))
	assert.JSONEq(t, `{"refresh_token":"rt-2"}`, out.String())
}
