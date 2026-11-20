package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery/bigquerytest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// A BigQuery Connection (issue #976): a service account's key, made in the test with a throwaway RSA key, checked
// against a fake Google token endpoint and a fake BigQuery. No real credential is anywhere here.

const (
	saEmail    = "hypomnema-ga4@data-project.iam.gserviceaccount.com"
	fakeToken  = "ya29.fake-access-token-of-the-test"
	ga4Dataset = "data-project.analytics_1"
)

// googleTokens is a fake of Google's token endpoint: it checks the JWT's grant type and answers with a token, or
// refuses with invalid_grant.
type googleTokens struct {
	*httptest.Server

	mu      sync.Mutex
	refuse  bool
	issued  int
	scopes  []string
	pemSeen bool
}

func newGoogleTokens(t *testing.T) *googleTokens {
	t.Helper()

	g := &googleTokens{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		g.mu.Lock()
		defer g.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if g.refuse || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))

			return
		}

		// The claims of the JWT, to see the scope asked.
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) == 3 {
			raw, _ := decodeSegment(parts[1])

			var claims struct {
				Scope string `json:"scope"`
			}

			_ = json.Unmarshal(raw, &claims)
			g.scopes = append(g.scopes, claims.Scope)
		}

		g.pemSeen = g.pemSeen || strings.Contains(r.Form.Encode(), "PRIVATE")
		g.issued++
		_, _ = w.Write([]byte(`{"access_token":"` + fakeToken + `","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(g.Close)

	return g
}

func decodeSegment(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// serviceAccountKey is a key file as Google gives it, with an RSA key made now.
func serviceAccountKey(t *testing.T, change func(map[string]any)) ([]byte, string) {
	t.Helper()

	private, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)

	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	key := map[string]any{
		"type": "service_account", "project_id": "key-project", "private_key_id": "abc123",
		"private_key": pemKey, "client_email": saEmail, "client_id": "1",
		"token_uri": "https://oauth2.googleapis.com/token",
	}
	if change != nil {
		change(key)
	}

	raw, err := json.Marshal(key)
	require.NoError(t, err)

	return raw, pemKey
}

type bigqueryWorld struct {
	*world

	tokens *googleTokens
	fake   *bigquerytest.Server
	logs   *bytes.Buffer
}

func newBigQueryWorld(t *testing.T) *bigqueryWorld {
	t.Helper()

	w := &bigqueryWorld{world: newWorld(t), tokens: newGoogleTokens(t), fake: bigquerytest.New(t), logs: &bytes.Buffer{}}
	w.fake.Tables[ga4Dataset] = []bigquerytest.Table{
		{ID: "a_view", Type: "VIEW"}, {ID: "events_20261118", Type: "TABLE"}, {ID: "events_20261119", Type: "TABLE"},
	}
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.BigQuery()).
		WithGoogle(w.tokens.URL, w.fake.URL).
		WithLogger(slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	return w
}

func (w *bigqueryWorld) connect(label string, key []byte, datasets ...string) (integration.Connection, error) {
	if len(datasets) == 0 {
		datasets = []string{ga4Dataset}
	}

	return w.svc.ConnectServiceAccount(w.t.Context(), integration.BigQueryID, label, integration.ServiceAccount{
		Key: key, Settings: bigquery.Settings{BillingProject: "billing-project", Datasets: datasets},
	})
}

// Scenario: the owner uploads a new key of the dedicated account with the GA4 dataset. The check passes step by
// step (the view listed first is passed over for the newest native events_ table); the Connection names the
// account and the settings with the cap's default; the key is sealed in the vault whole; and a query through it
// carries the token the control plane got, which asked for the bigquery scope alone.
func TestTheOwnerConnectsBigQueryWithAServiceAccountKey(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	key, pemKey := serviceAccountKey(t, nil)

	conn, err := w.connect("ga4", key)
	require.NoError(t, err)
	assert.Equal(t, saEmail, conn.Account)
	require.NotNil(t, conn.BigQuery)
	assert.Equal(t, bigquery.Settings{
		BillingProject: "billing-project", Datasets: []string{ga4Dataset}, MaxBytesBilled: bigquery.DefaultMaxBytes,
	}, *conn.BigQuery)
	assert.False(t, conn.SignInRequired)

	secrets, err := w.vault.List(t.Context())
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	assert.Equal(t, "integration-bigquery-ga4", secrets[0].Name)

	opened, err := w.vault.Open(t.Context(), "integration-bigquery-ga4")
	require.NoError(t, err)
	assert.JSONEq(t, string(key), opened.Payload["value"], "the key is kept whole")
	assert.NotContains(t, w.stored(), "PRIVATE KEY", "and sealed")

	assert.Equal(t, []string{"https://www.googleapis.com/auth/bigquery"}, w.tokens.scopes)

	for _, r := range w.fake.Requests() {
		assert.Equal(t, "Bearer "+fakeToken, r.Authorization)
		assert.NotContains(t, r.Path, "a_view")
	}

	assert.Empty(t, w.fake.Jobs(), "the check bills nothing")

	w.fake.Columns, w.fake.Rows = []string{"n"}, [][]string{{"1"}}

	err = w.svc.WithBigQuery(t.Context(), integration.BigQueryID, "ga4", func(c *bigquery.Client) error {
		got, err := c.Query(t.Context(), bigquery.Request{SQL: "SELECT 1"})
		assert.Equal(t, [][]any{{"1"}}, got.Rows)

		return err
	})
	require.NoError(t, err)
	assert.Equal(t, 1, w.tokens.issued, "the token is kept in memory")

	logs := w.logs.String()
	assert.Contains(t, logs, `"event":"bigquery.job"`)
	assert.NotContains(t, logs, fakeToken)
	assert.NotContains(t, logs, pemKey[40:80])
	assert.NotContains(t, logs, "private_key")
}

// Scenario: each failing step of the check names itself and stores nothing; a key not a service account's, or
// naming another token address, is refused before anything is sent.
func TestAFailingCheckStoresNothing(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		setup func(w *bigqueryWorld)
		key   func(map[string]any)
		step  string
	}{
		"a refused token": {setup: func(w *bigqueryWorld) { w.tokens.refuse = true }, step: "token's exchange"},
		"a dataset holding an external table": {setup: func(w *bigqueryWorld) {
			w.fake.Tables[ga4Dataset] = append(w.fake.Tables[ga4Dataset], bigquerytest.Table{ID: "gcs", Type: "EXTERNAL"})
		}, step: "listing the tables of " + ga4Dataset},
		"a dataset of views alone": {setup: func(w *bigqueryWorld) {
			w.fake.Tables[ga4Dataset] = []bigquerytest.Table{{ID: "v", Type: "VIEW"}}
		}, step: "no native table"},
		"a dataset not granted": {setup: func(w *bigqueryWorld) { delete(w.fake.Tables, ga4Dataset) },
			step: "listing the tables"},
		"a row that cannot be read": {setup: func(w *bigqueryWorld) { w.fake.RowStatus = 403 },
			step: "reading a row of " + ga4Dataset + ".events_20261119"},
		"a dry run refused in the billing project": {setup: func(w *bigqueryWorld) { w.fake.DryRunStatus = 403 },
			step: "a dry run in project billing-project"},
		"a key of another type": {key: func(k map[string]any) { k["type"] = "authorized_user" }, step: "service_account"},
		"another token address": {key: func(k map[string]any) { k["token_uri"] = "https://evil.example/token" },
			step: "token_uri"},
		"no private key": {key: func(k map[string]any) { k["private_key"] = "not a key" }, step: "private_key"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newBigQueryWorld(t)
			if c.setup != nil {
				c.setup(w)
			}

			key, pemKey := serviceAccountKey(t, c.key)

			_, err := w.connect("ga4", key)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.step)
			assert.NotContains(t, err.Error(), pemKey[40:80])
			assert.NotContains(t, err.Error(), fakeToken)

			secrets, err := w.vault.List(t.Context())
			require.NoError(t, err)
			assert.Empty(t, secrets, "a failed check keeps no key")

			all, err := w.svc.List(t.Context())
			require.NoError(t, err)
			assert.Empty(t, all[0].Connections)
			assert.Empty(t, w.fake.Jobs())
		})
	}
}

// Scenario: connecting the label again with a new key rotates the secret and the token is made anew; the cap is
// changed; a check again passes; a token Google then refuses marks the Connection; disconnecting deletes the key.
func TestABigQueryConnectionIsRotatedCheckedAndDisconnected(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	ctx := t.Context()
	first, _ := serviceAccountKey(t, nil)
	second, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", first)
	require.NoError(t, err)

	_, err = w.connect("ga4", second)
	require.NoError(t, err)

	secrets, err := w.vault.List(ctx)
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	assert.Equal(t, 2, secrets[0].Version, "the secret was rotated")

	opened, err := w.vault.Open(ctx, "integration-bigquery-ga4")
	require.NoError(t, err)
	assert.JSONEq(t, string(second), opened.Payload["value"])

	changed, err := w.svc.SetMaxBytes(ctx, integration.BigQueryID, "ga4", 1<<30)
	require.NoError(t, err)
	assert.Equal(t, int64(1<<30), changed.BigQuery.MaxBytesBilled)

	_, err = w.svc.SetMaxBytes(ctx, integration.BigQueryID, "ga4", 0)
	require.ErrorIs(t, err, integration.ErrInvalid)

	again, err := w.svc.Check(ctx, integration.BigQueryID, "ga4")
	require.Error(t, err, "no checker is wired in this world")
	assert.Empty(t, again.Label)

	w.svc.WithChecker(func(_ context.Context, _, _ string) error { return nil })

	_, err = w.svc.Check(ctx, integration.BigQueryID, "ga4")
	require.NoError(t, err)

	w.tokens.mu.Lock()
	w.tokens.refuse = true
	w.tokens.mu.Unlock()

	// The token in memory still serves; a new key's exchange is refused.
	third, _ := serviceAccountKey(t, nil)
	_, err = w.connect("ga4", third)
	require.ErrorIs(t, err, integration.ErrSignInFailed)

	opened, err = w.vault.Open(ctx, "integration-bigquery-ga4")
	require.NoError(t, err)
	assert.JSONEq(t, string(second), opened.Payload["value"], "a refused key replaces nothing")

	revoked, err := w.svc.Disconnect(ctx, integration.BigQueryID, "ga4")
	require.NoError(t, err)
	assert.False(t, revoked, "the key still exists at Google")

	secrets, err = w.vault.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, secrets, "disconnecting deletes the key")
}

// Scenario: Google stops giving a token for the key kept: a query fails asking for the Connection to be connected
// again, and the Connection says so.
func TestARefusedTokenAsksToConnectAgain(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	key, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", key)
	require.NoError(t, err)

	// A new control plane: nothing in memory.
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.BigQuery()).
		WithGoogle(w.tokens.URL, w.fake.URL)
	w.tokens.mu.Lock()
	w.tokens.refuse = true
	w.tokens.mu.Unlock()

	err = w.svc.WithBigQuery(t.Context(), integration.BigQueryID, "ga4", func(c *bigquery.Client) error {
		_, err := c.Query(t.Context(), bigquery.Request{SQL: "SELECT 1"})

		return err
	})
	require.ErrorIs(t, err, integration.ErrSignInRequired)
	assert.NotContains(t, err.Error(), "Invalid JWT Signature", "only Google's error code")

	all, err := w.svc.List(t.Context())
	require.NoError(t, err)
	assert.True(t, all[0].Connections[0].SignInRequired)
}
