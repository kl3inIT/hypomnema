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
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery/bigquerytest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// A BigQuery Connection (issue #976): a service account's key, made in the test with a throwaway RSA key, checked
// against a fake Google token endpoint and a fake BigQuery. No real credential is anywhere here.

const (
	saEmail    = "hypomnema-ga4@data-project.iam.gserviceaccount.com"
	fakeToken  = "ya29.fake-access-token-of-the-test" //nolint:gosec // A made-up token of a test.
	ga4Dataset = "data-project.analytics_1"
)

// googleTokens is a fake of Google's token endpoint: it checks the JWT's grant type and answers with a token, or
// refuses with invalid_grant.
type googleTokens struct {
	*httptest.Server

	mu     sync.Mutex
	refuse bool
	// unavailable answers 503; delay holds each answer, or until the caller gives up.
	unavailable bool
	delay       time.Duration
	// echo is an error code the endpoint answers 400 with, as a provider that repeats what it was sent;
	// perIssuer gives each account's token its own value, "<fake token>-<client_email>".
	echo      string
	perIssuer bool
	issued    int
	scopes    []string
	pemSeen   bool
}

func newGoogleTokens(t *testing.T) *googleTokens {
	t.Helper()

	g := &googleTokens{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		g.mu.Lock()
		delay := g.delay
		g.mu.Unlock()

		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}

		g.mu.Lock()
		defer g.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if g.echo != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"` + g.echo + `","error_description":"` + g.echo + `"}`))

			return
		}

		if g.unavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))

			return
		}

		if g.refuse || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))

			return
		}

		// The claims of the JWT, to see the scope asked and who asks.
		issuer := ""
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) == 3 {
			raw, _ := decodeSegment(parts[1])

			var claims struct {
				Scope string `json:"scope"`
				Iss   string `json:"iss"`
			}

			_ = json.Unmarshal(raw, &claims)
			g.scopes = append(g.scopes, claims.Scope)
			issuer = claims.Iss
		}

		g.pemSeen = g.pemSeen || strings.Contains(r.Form.Encode(), "PRIVATE")
		g.issued++
		token := fakeToken
		if g.perIssuer {
			token += "-" + issuer
		}

		_, _ = w.Write([]byte(`{"access_token":"` + token + `","token_type":"Bearer","expires_in":3600}`))
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

	//nolint:gosec // A key made by this test, with a throwaway RSA key.
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

func (g *googleTokens) set(change func(g *googleTokens)) {
	g.mu.Lock()
	defer g.mu.Unlock()

	change(g)
}

func (g *googleTokens) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.issued
}

// query runs one query through the Connection.
func (w *bigqueryWorld) query(label string) error {
	return w.svc.WithBigQuery(w.t.Context(), integration.BigQueryID, label, func(c *bigquery.Client) error {
		_, err := c.Query(w.t.Context(), bigquery.Request{SQL: "SELECT 1"})

		return err
	})
}

// restart is a new control plane on the same database: nothing in memory.
func (w *bigqueryWorld) restart() {
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.BigQuery()).
		WithGoogle(w.tokens.URL, w.fake.URL)
}

func (w *bigqueryWorld) needsSignIn(label string) bool {
	w.t.Helper()

	all, err := w.svc.List(w.t.Context())
	require.NoError(w.t, err)

	for _, c := range all[0].Connections {
		if c.Label == label {
			return c.SignInRequired
		}
	}

	w.t.Fatalf("no connection %s", label)

	return false
}

// Scenario (review round 1): Google's token endpoint is slow. The exchange runs on the call's own context, so a
// call with a short deadline ends at its deadline, not at the HTTP client's 30 seconds.
func TestTheTokenExchangeEndsWithItsCall(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	key, _ := serviceAccountKey(t, nil)
	w.tokens.set(func(g *googleTokens) { g.delay = 5 * time.Second })

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := w.svc.ConnectServiceAccount(ctx, integration.BigQueryID, "ga4", integration.ServiceAccount{
		Key: key, Settings: bigquery.Settings{BillingProject: "billing-project", Datasets: []string{ga4Dataset}},
	})
	require.ErrorContains(t, err, "the token's exchange")
	assert.Less(t, time.Since(start), 2*time.Second, "the exchange ended with the call's deadline")
}

// Scenario (review round 1): Google's token endpoint answers 503 for a while. A query fails and says to try
// again, the Connection is not marked as needing to be connected again, and once Google is back it works.
func TestAPassingTokenOutageMarksNothing(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	key, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", key)
	require.NoError(t, err)

	w.restart()
	w.tokens.set(func(g *googleTokens) { g.unavailable = true })

	err = w.query("ga4")
	require.Error(t, err)
	require.NotErrorIs(t, err, integration.ErrSignInRequired)
	assert.Contains(t, err.Error(), "503")
	assert.False(t, w.needsSignIn("ga4"))

	_, err = w.svc.Check(t.Context(), integration.BigQueryID, "ga4")
	require.Error(t, err, "no checker here, and the exchange failed")
	assert.False(t, w.needsSignIn("ga4"), "a check that met a 503 marks nothing either")

	w.tokens.set(func(g *googleTokens) { g.unavailable = false })
	w.fake.Columns = []string{"n"}
	require.NoError(t, w.query("ga4"))
}

// Scenario (review round 1): a call made with key A ends in Google refusing A after the owner replaced it with
// key B. The refusal says nothing of B: the Connection is not marked and B's token stays.
func TestARefusalOfAReplacedKeyMarksNothing(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	first, _ := serviceAccountKey(t, nil)
	second, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", first)
	require.NoError(t, err)

	err = w.svc.WithBigQuery(t.Context(), integration.BigQueryID, "ga4", func(*bigquery.Client) error {
		_, err := w.connect("ga4", second)
		require.NoError(t, err)

		return &bigquery.TokenError{Refused: true, Message: "Google refused the service account's key: 400 invalid_grant"}
	})
	require.ErrorIs(t, err, integration.ErrSignInRequired, "the call itself failed")
	assert.False(t, w.needsSignIn("ga4"), "the key that replaced it is not marked")

	issued := w.tokens.count()
	w.fake.Columns = []string{"n"}
	require.NoError(t, w.query("ga4"))
	assert.Equal(t, issued, w.tokens.count(), "B's token is kept: no new exchange")

	// A refusal of the key in use marks it.
	err = w.svc.WithBigQuery(t.Context(), integration.BigQueryID, "ga4", func(*bigquery.Client) error {
		return &bigquery.TokenError{Refused: true, Message: "refused"}
	})
	require.ErrorIs(t, err, integration.ErrSignInRequired)
	assert.True(t, w.needsSignIn("ga4"))
}

// failingStore refuses to save a Connection, as a database that fails at the last write.
type failingStore struct {
	*orchestration.Service
}

func (failingStore) SaveServiceConnection(context.Context, orchestration.Connection) (orchestration.Connection, error) {
	return orchestration.Connection{}, errors.New("the database is gone")
}

// Scenario (review round 1): the Connection cannot be saved after its key was written. A new label leaves no
// key behind; a replaced key is put back, and the Connection keeps working with it.
func TestAConnectionThatCannotBeSavedLeavesTheVaultAsItWas(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	first, _ := serviceAccountKey(t, nil)
	second, _ := serviceAccountKey(t, nil)
	good := w.svc

	w.svc = integration.New(failingStore{w.orch}, w.vault, w.clock, platform.NewID, integration.BigQuery()).
		WithGoogle(w.tokens.URL, w.fake.URL)

	_, err := w.connect("ga4", first)
	require.ErrorContains(t, err, "the database is gone")

	secrets, err := w.vault.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, secrets, "a new label leaves no key")

	w.svc = good
	_, err = w.connect("ga4", first)
	require.NoError(t, err)

	w.svc = integration.New(failingStore{w.orch}, w.vault, w.clock, platform.NewID, integration.BigQuery()).
		WithGoogle(w.tokens.URL, w.fake.URL)
	_, err = w.connect("ga4", second)
	require.ErrorContains(t, err, "the database is gone")

	opened, err := w.vault.Open(t.Context(), "integration-bigquery-ga4")
	require.NoError(t, err)
	assert.JSONEq(t, string(first), opened.Payload["value"], "the replaced key is put back")

	w.svc = good
	w.restart()
	w.fake.Columns = []string{"n"}
	require.NoError(t, w.query("ga4"), "the Connection works with its key")
}

// Scenario (review round 1): a disconnect removed the Connection but not its key. Disconnecting again finishes
// it; then there is nothing left to disconnect.
func TestADisconnectThatLeftItsKeyCanBeFinished(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	key, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", key)
	require.NoError(t, err)

	_, err = w.orch.RemoveConnection(t.Context(), integration.BigQueryID, "ga4")
	require.NoError(t, err)

	revoked, err := w.svc.Disconnect(t.Context(), integration.BigQueryID, "ga4")
	require.NoError(t, err)
	assert.False(t, revoked)

	secrets, err := w.vault.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, secrets)

	_, err = w.svc.Disconnect(t.Context(), integration.BigQueryID, "ga4")
	require.ErrorIs(t, err, integration.ErrNotFound)
}

// Scenario (review round 1): reconnects, disconnects and cap changes of one label run at the same time. Whatever
// their order, a Connection and its key exist together or not at all; a cap change never undoes a reconnect's
// datasets; and a reconnect that names no cap keeps the label's.
func TestALabelsWritesDoNotInterleave(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	w.fake.Tables["data-project.analytics_2"] = w.fake.Tables[ga4Dataset]
	key, _ := serviceAccountKey(t, nil)
	ctx := t.Context()

	_, err := w.connect("ga4", key)
	require.NoError(t, err)

	for range 5 {
		var wg sync.WaitGroup

		wg.Go(func() { _, _ = w.connect("ga4", key, "data-project.analytics_2") })
		wg.Go(func() { _, _ = w.svc.SetMaxBytes(ctx, integration.BigQueryID, "ga4", 2<<30) })
		wg.Wait()

		conn, err := w.orch.ConnectionOf(ctx, integration.BigQueryID, "ga4")
		require.NoError(t, err)

		var settings bigquery.Settings
		require.NoError(t, json.Unmarshal([]byte(conn.Settings), &settings))
		assert.Equal(t, []string{"data-project.analytics_2"}, settings.Datasets, "the reconnect's datasets stay")
		assert.Equal(t, int64(2<<30), settings.MaxBytesBilled, "the cap set stays")

		wg.Go(func() { _, _ = w.connect("ga4", key) })
		wg.Go(func() { _, _ = w.svc.Disconnect(ctx, integration.BigQueryID, "ga4") })
		wg.Wait()

		_, connErr := w.orch.ConnectionOf(ctx, integration.BigQueryID, "ga4")
		_, keyErr := w.vault.Get(ctx, "integration-bigquery-ga4")
		assert.Equal(t, connErr == nil, keyErr == nil, "a Connection and its key go together")

		if connErr != nil {
			_, err = w.connect("ga4", key)
			require.NoError(t, err)
			_, err = w.svc.SetMaxBytes(ctx, integration.BigQueryID, "ga4", 2<<30)
			require.NoError(t, err)
		}
	}
}

// Scenario: no agent is handed a service account's token through the token route's source.
func TestAServiceAccountHandsOutNoToken(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	key, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", key)
	require.NoError(t, err)

	got, err := w.svc.Token(t.Context(), integration.BigQueryID, "ga4")
	require.ErrorIs(t, err, integration.ErrInvalid)
	assert.Empty(t, got.AccessToken)
}

// connectTo connects a label with a key and a billing project of its own.
func (w *bigqueryWorld) connectTo(ctx context.Context, label string, key []byte, billing string,
) (integration.Connection, error) {
	return w.svc.ConnectServiceAccount(ctx, integration.BigQueryID, label, integration.ServiceAccount{
		Key: key, Settings: bigquery.Settings{BillingProject: billing, Datasets: []string{ga4Dataset}},
	})
}

// Scenario (review round 2, P1): Google's token endpoint answers with an error that repeats what it was sent.
// Only a known OAuth code is repeated: the echoed text reaches no error the API answers with, nor the log.
func TestAnEchoingTokenEndpointRepeatsNothing(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	w.tokens.set(func(g *googleTokens) { g.echo = "ECHOEDsecretAKIAvalue987" })
	key, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", key)
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.NotContains(t, err.Error(), "ECHOEDsecretAKIAvalue987")
	assert.Contains(t, err.Error(), "400 error")
	assert.NotContains(t, w.logs.String(), "ECHOEDsecretAKIAvalue987")

	w.tokens.set(func(g *googleTokens) { g.echo = "invalid_grant" })

	_, err = w.connect("ga4", key)
	require.ErrorContains(t, err, "400 invalid_grant", "a known code is said")
}

// Scenario (review round 2, CodeRabbit #4641253075): one call exchanges the token slowly; another, whose
// deadline is short, gives up at its own deadline instead of waiting for the first's exchange.
func TestACallWaitingForTheTokenGivesUpAtItsDeadline(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	key, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", key)
	require.NoError(t, err)

	w.restart()
	w.tokens.set(func(g *googleTokens) { g.delay = 2 * time.Second })

	slow := make(chan error, 1)
	go func() { slow <- w.query("ga4") }()

	time.Sleep(200 * time.Millisecond) // the first call holds the exchange

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = w.svc.WithBigQuery(ctx, integration.BigQueryID, "ga4", func(c *bigquery.Client) error {
		_, err := c.Query(ctx, bigquery.Request{SQL: "SELECT 1"})

		return err
	})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 600*time.Millisecond, "it gave up at its own deadline")

	w.tokens.set(func(g *googleTokens) { g.delay = 0 })
	<-slow
}

// cancelingStore cancels the request as the Connection is saved, as a client that hung up after the key was
// written; fail makes the save fail too.
type cancelingStore struct {
	*orchestration.Service

	cancel context.CancelFunc
	fail   bool
}

func (c cancelingStore) SaveServiceConnection(ctx context.Context, conn orchestration.Connection,
) (orchestration.Connection, error) {
	c.cancel()

	if c.fail {
		return orchestration.Connection{}, errors.New("the database is gone")
	}

	return c.Service.SaveServiceConnection(ctx, conn)
}

// Scenario (review round 2, CodeRabbit #4641253084): the request is canceled right after the key was written.
// The writes still go to their end: the Connection is saved with its key, or, when the save fails, the key made
// is removed and a replaced key is put back; never a key without its Connection, nor a new key beside old
// settings.
func TestACanceledRequestLeavesKeyAndConnectionTogether(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	first, _ := serviceAccountKey(t, nil)
	second, _ := serviceAccountKey(t, nil)
	good := w.svc

	with := func(fail bool) context.Context {
		ctx, cancel := context.WithCancel(t.Context())
		w.svc = integration.New(cancelingStore{Service: w.orch, cancel: cancel, fail: fail}, w.vault, w.clock,
			platform.NewID, integration.BigQuery()).WithGoogle(w.tokens.URL, w.fake.URL)

		return ctx
	}

	_, err := w.connectTo(with(false), "ga4", first, "billing-one")
	require.NoError(t, err, "the save is not the request's to cancel")

	conn, err := w.orch.ConnectionOf(t.Context(), integration.BigQueryID, "ga4")
	require.NoError(t, err)
	assert.Contains(t, conn.Settings, "billing-one")

	_, err = w.connectTo(with(true), "ga4", second, "billing-two")
	require.ErrorContains(t, err, "the database is gone")

	opened, err := w.vault.Open(t.Context(), "integration-bigquery-ga4")
	require.NoError(t, err)
	assert.JSONEq(t, string(first), opened.Payload["value"], "the replaced key is back beside its settings")

	conn, err = w.orch.ConnectionOf(t.Context(), integration.BigQueryID, "ga4")
	require.NoError(t, err)
	assert.Contains(t, conn.Settings, "billing-one")

	_, err = w.connectTo(with(true), "other", second, "billing-two")
	require.ErrorContains(t, err, "the database is gone")

	_, err = w.vault.Get(t.Context(), "integration-bigquery-other")
	require.ErrorIs(t, err, vault.ErrNotFound, "no key is left without its Connection")

	w.svc = good
}

// Scenario (review round 2, CodeRabbit #4641253092): a reconnect with another key and another billing project
// comes while a call reads the Connection. The call pairs the key and the settings of one state: every request
// carrying A's token goes to A's billing project, and B's to B's.
func TestACallReadsKeyAndSettingsFromOneState(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	w.tokens.set(func(g *googleTokens) { g.perIssuer = true })
	keyA, _ := serviceAccountKey(t, func(k map[string]any) { k["client_email"] = "a@data-project.iam.gserviceaccount.com" })
	keyB, _ := serviceAccountKey(t, func(k map[string]any) { k["client_email"] = "b@data-project.iam.gserviceaccount.com" })

	_, err := w.connectTo(t.Context(), "ga4", keyA, "billing-aaa")
	require.NoError(t, err)

	w.restart()

	reconnected := make(chan error, 1)
	once := sync.Once{}

	w.svc.BetweenReads(func() {
		once.Do(func() {
			go func() {
				_, err := w.connectTo(context.Background(), "ga4", keyB, "billing-bbb")
				reconnected <- err
			}()

			// A reconnect that could run now would pair B's key with A's settings: it is given the time to.
			select {
			case err := <-reconnected:
				reconnected <- err
			case <-time.After(time.Second):
			}
		})
	})

	w.fake.Columns = []string{"n"}
	require.NoError(t, w.query("ga4"))
	require.NoError(t, <-reconnected)

	for _, r := range w.fake.Requests() {
		switch {
		case strings.HasSuffix(r.Authorization, "-a@data-project.iam.gserviceaccount.com"):
			assert.NotContains(t, r.Path, "billing-bbb", "A's token went to B's project")
		case strings.HasSuffix(r.Authorization, "-b@data-project.iam.gserviceaccount.com"):
			assert.NotContains(t, r.Path, "billing-aaa", "B's token went to A's project")
		}
	}
}

// Scenario (review round 2, CodeRabbit #4641253108): the label is disconnected and connected again with another
// key, whose secret starts again at version 1. A refusal of the old key, arriving afterwards, marks nothing.
func TestARefusalOfAKeyMadeAgainUnderItsNameMarksNothing(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t)
	first, _ := serviceAccountKey(t, nil)
	second, _ := serviceAccountKey(t, nil)

	_, err := w.connect("ga4", first)
	require.NoError(t, err)

	err = w.svc.WithBigQuery(t.Context(), integration.BigQueryID, "ga4", func(*bigquery.Client) error {
		_, err := w.svc.Disconnect(t.Context(), integration.BigQueryID, "ga4")
		require.NoError(t, err)

		_, err = w.connect("ga4", second)
		require.NoError(t, err)

		made, err := w.vault.Get(t.Context(), "integration-bigquery-ga4")
		require.NoError(t, err)
		require.Equal(t, 1, made.Version, "made again at version 1")

		return &bigquery.TokenError{Refused: true, Message: "Google refused the service account's key: 400 invalid_grant"}
	})
	require.ErrorIs(t, err, integration.ErrSignInRequired)
	assert.False(t, w.needsSignIn("ga4"), "the key made again is not marked")
}
