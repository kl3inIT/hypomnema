package controlplane

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

func TestTheOwnerMakesEditsReadsAndRevokesAGatewayKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	keyring, err := vault.ParseKeyring(keygen(t, "test"), "test")
	require.NoError(t, err)
	referrers := secretReferrers(svc)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, keyring, referrers)
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, gatewayProviders(svc),
		func(context.Context, string) (string, error) { return "", nil }).WithKeyVault(secrets)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, slog.New(slog.DiscardHandler), stop).WithGatewayKeys(gateway).
		WithSecrets(ownerSecrets{vault: secrets, referrers: referrers}).Register(mux))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := func(kind bearertoken.Kind, name string) *openapi.ClientWithResponses {
		_, secret, err := svc.AddToken(ctx, name, kind)
		require.NoError(t, err)

		c, err := openapi.NewClientWithResponses(srv.URL+clientapi.BasePath,
			openapi.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
				r.Header.Set("Authorization", "Bearer "+secret)

				return nil
			}))
		require.NoError(t, err)

		return c
	}

	owner := client(bearertoken.Owner, "web")
	pi := client(bearertoken.Coordinator, "pi")

	workerKey, err := gateway.AddDispatchKey(ctx, "dsp_1", "gpt-sol")
	require.NoError(t, err)

	refused, err := pi.CreateGatewayKeyWithResponse(ctx, openapi.NewGatewayKey{Name: "opencode-laptop"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	bad, err := owner.CreateGatewayKeyWithResponse(ctx, openapi.NewGatewayKey{Name: "Not A Name"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())

	made, err := owner.CreateGatewayKeyWithResponse(ctx, openapi.NewGatewayKey{Name: "opencode-laptop"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))

	secret := made.JSON201.Secret
	id := made.JSON201.Key.Id
	assert.True(t, strings.HasPrefix(secret, made.JSON201.Key.Prefix), "the start shown is the key's own")
	assert.Len(t, made.JSON201.Key.Prefix, len("hyp_gateway_v1_")+6)
	assert.Equal(t, []string{modelgateway.AllModels}, made.JSON201.Key.Models, "no model named: every model")
	masked := secret[:6] + strings.Repeat("•", len(secret)-10) + secret[len(secret)-4:]
	require.NotNil(t, made.JSON201.Key.MaskedValue)
	assert.Equal(t, masked, *made.JSON201.Key.MaskedValue)

	var keyHash, versionID, storedMask, secretID string
	var ciphertext []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT k.key_hash, k.secret_version_id, k.masked_value,
		v.secret_id, v.ciphertext FROM gateway_keys k JOIN secret_versions v ON v.id = k.secret_version_id
		WHERE k.id = ?`, id).Scan(&keyHash, &versionID, &storedMask, &secretID, &ciphertext))
	assert.Equal(t, bearertoken.Hash(secret), keyHash)
	assert.Equal(t, masked, storedMask)
	assert.NotEmpty(t, ciphertext)
	assert.False(t, bytes.Contains(ciphertext, []byte(secret)))
	assert.NotContains(t, versionID, secret)

	works, err := gateway.Authenticate(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, "opencode-laptop", works.Name)

	listed, err := owner.ListGatewayKeysWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	require.Len(t, listed.JSON200.Data, 1, "a worker's key is not listed")
	assert.Equal(t, "opencode-laptop", listed.JSON200.Data[0].Name)
	assert.Nil(t, listed.JSON200.Data[0].LastUsedAt)
	assert.Equal(t, 1, listed.JSON200.LiveWorkerKeys)
	assert.NotContains(t, string(listed.Body), secret)

	usedAt := time.Now().UTC().Truncate(time.Millisecond)
	_, err = db.ExecContext(ctx, `INSERT INTO gateway_usage
		(id, key_id, model, provider, upstream_model, stream, attempts, outcome, status, duration_ms, created_at)
		VALUES ('request-1', ?, 'old-model', 'provider', 'upstream', 0, 1, 'ok', 200, 5, ?)`, id, usedAt.UnixMilli())
	require.NoError(t, err)

	newName := "opencode-tool"
	models := []string{"gpt-sol"}
	endpoints := []openapi.GatewayEndpointSelection{openapi.GatewayEndpointSelectionAudio}
	change := openapi.GatewayKeyUpdate{Name: &newName, Models: &models, Endpoints: &endpoints}
	refusedEdit, err := pi.UpdateGatewayKeyWithResponse(ctx, id, change)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refusedEdit.StatusCode())
	refusedRead, err := pi.GetGatewayKeyValueWithResponse(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refusedRead.StatusCode())

	updated, err := owner.UpdateGatewayKeyWithResponse(ctx, id, change)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, updated.StatusCode(), string(updated.Body))
	assert.NotContains(t, string(updated.Body), secret)
	assert.Equal(t, id, updated.JSON200.Id)
	assert.Equal(t, made.JSON201.Key.CreatedAt, updated.JSON200.CreatedAt)
	require.NotNil(t, updated.JSON200.LastUsedAt)
	assert.Equal(t, usedAt, *updated.JSON200.LastUsedAt)
	assert.Equal(t, newName, updated.JSON200.Name)

	works, err = gateway.Authenticate(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, id, works.ID)
	assert.True(t, works.Allows("gpt-sol"))
	assert.False(t, works.Allows("old-model"))
	assert.True(t, works.Reaches(modelgateway.EndpointAudio))
	assert.False(t, works.Reaches(modelgateway.EndpointSearch))

	value, err := owner.GetGatewayKeyValueWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, value.StatusCode())
	assert.Equal(t, "no-store", value.HTTPResponse.Header.Get("Cache-Control"))
	assert.Equal(t, secret, value.JSON200.Secret)

	// Connect a tool (issue #907): the owner's alone, not cached, the key's value in the file.
	connect := &openapi.GetGatewayToolConfigParams{BaseUrl: "https://hyp.example"}
	refusedConnect, err := pi.GetGatewayToolConfigWithResponse(ctx, id, openapi.GatewayToolCodex, connect)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refusedConnect.StatusCode())

	codex, err := owner.GetGatewayToolConfigWithResponse(ctx, id, openapi.GatewayToolCodex, connect)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, codex.StatusCode(), string(codex.Body))
	assert.Equal(t, "no-store", codex.HTTPResponse.Header.Get("Cache-Control"))
	assert.True(t, codex.JSON200.KeyStored)
	require.Len(t, codex.JSON200.Files, 1)
	assert.Equal(t, "~/.codex/config.toml", codex.JSON200.Files[0].Path)
	assert.Equal(t, "toml", codex.JSON200.Files[0].Format)
	assert.Contains(t, codex.JSON200.Files[0].Content, secret)
	assert.Contains(t, codex.JSON200.Files[0].Content, `"https://hyp.example/v1/codex/models"`)

	badBase, err := owner.GetGatewayToolConfigWithResponse(ctx, id, openapi.GatewayToolOmp,
		&openapi.GetGatewayToolConfigParams{BaseUrl: "not an address"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, badBase.StatusCode())

	secretName := strings.ReplaceAll(id, "_", "-")
	vaultList, err := owner.ListSecretsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, vaultList.StatusCode(), string(vaultList.Body))
	require.Len(t, vaultList.JSON200.Data, 1)
	assert.Equal(t, secretName, vaultList.JSON200.Data[0].Name)
	assert.Equal(t, []string{"gateway key " + newName}, vaultList.JSON200.Data[0].UsedBy)
	assert.NotContains(t, string(vaultList.Body), secret)
	deleted, err := owner.DeleteSecretWithResponse(ctx, secretName)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, deleted.StatusCode(), string(deleted.Body))
	assert.Contains(t, string(deleted.Body), "used by gateway key "+newName)

	emptyEndpoints := []openapi.GatewayEndpointSelection{}
	partial, err := owner.UpdateGatewayKeyWithResponse(ctx, id, openapi.GatewayKeyUpdate{Endpoints: &emptyEndpoints})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, partial.StatusCode())
	assert.Equal(t, newName, partial.JSON200.Name)
	assert.Equal(t, models, partial.JSON200.Models)
	assert.Empty(t, partial.JSON200.Endpoints)

	allEndpoints := []openapi.GatewayEndpointSelection{openapi.GatewayEndpointSelectionAsterisk}
	unrestricted, err := owner.UpdateGatewayKeyWithResponse(ctx, id, openapi.GatewayKeyUpdate{Endpoints: &allEndpoints})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, unrestricted.StatusCode(), string(unrestricted.Body))
	assert.Len(t, unrestricted.JSON200.Endpoints, len(modelgateway.Endpoints))
	var storedEndpoints *string
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT endpoints FROM gateway_keys WHERE id = ?", id).Scan(&storedEndpoints))
	assert.Nil(t, storedEndpoints)

	worker, err := gateway.Authenticate(ctx, workerKey)
	require.NoError(t, err)
	chat, _, err := gateway.AddChatKey(ctx, "cht_1", "", "gpt-sol")
	require.NoError(t, err)
	bot, _, err := gateway.AddChatKey(ctx, "cht_2", "bot_1", "gpt-sol")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE gateway_keys SET chat_id = NULL WHERE id = ?", bot.ID)
	require.NoError(t, err, "a Bot id alone must still exclude its key")
	for _, internalID := range []string{worker.ID, chat.ID, bot.ID} {
		edit, err := owner.UpdateGatewayKeyWithResponse(ctx, internalID, change)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, edit.StatusCode(), internalID)
		read, err := owner.GetGatewayKeyValueWithResponse(ctx, internalID)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, read.StatusCode(), internalID)
	}

	notOwners, err := owner.RevokeGatewayKeyWithResponse(ctx, worker.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, notOwners.StatusCode(), "a worker's key ends with its Dispatch")

	revoked, err := owner.RevokeGatewayKeyWithResponse(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, revoked.StatusCode())
	_, err = secrets.OpenVersion(ctx, versionID)
	require.ErrorIs(t, err, vault.ErrNotFound)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM secrets WHERE id = ?", secretID).Scan(&count))
	assert.Zero(t, count)
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT count(*) FROM gateway_keys WHERE id = ? AND secret_version_id IS NOT NULL", id).Scan(&count))
	assert.Zero(t, count)
	var usageKey string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT key_id FROM gateway_usage WHERE id = 'request-1'").Scan(&usageKey))
	assert.Equal(t, id, usageKey)

	_, err = gateway.Authenticate(ctx, secret)
	require.Error(t, err, "a revoked key no longer works")

	again, err := owner.RevokeGatewayKeyWithResponse(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, again.StatusCode())

	listed, err = owner.ListGatewayKeysWithResponse(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed.JSON200.Data)

	legacy, err := bearertoken.Generate(bearertoken.Gateway)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO gateway_keys (id, name, key_hash, key_prefix, models, created_at)
		VALUES ('gwk_old', 'old-key', ?, ?, '["*"]', ?)`, bearertoken.Hash(legacy), legacy[:21], usedAt.UnixMilli())
	require.NoError(t, err)
	oldValue, err := owner.GetGatewayKeyValueWithResponse(ctx, "gwk_old")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, oldValue.StatusCode())
	assert.Contains(t, string(oldValue.Body), "no stored value")
	oldEdit, err := owner.UpdateGatewayKeyWithResponse(ctx, "gwk_old", openapi.GatewayKeyUpdate{Models: &models})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, oldEdit.StatusCode(), string(oldEdit.Body))
	assert.Nil(t, oldEdit.JSON200.MaskedValue)
}

func TestGatewayKeyCLIWithoutVaultAndWithEveryEndpoint(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, nil, nil)
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, nil, nil).WithKeyVault(secrets)
	var output bytes.Buffer
	cli := owner{gateway: gateway, out: &output}
	require.NoError(t, cli.gatewayCommand(ctx, []string{"keys", "add", "laptop", "--endpoints", "search"}))
	assert.Contains(t, output.String(), "the value is not stored, so copy it now")
	_, secret, ok := strings.Cut(strings.TrimSpace(output.String()), "copy it now:\n")
	require.True(t, ok)
	key, err := gateway.Authenticate(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.Endpoint{modelgateway.EndpointSearch}, key.Endpoints)
	assert.Empty(t, key.MaskedValue)
	_, err = gateway.KeyValue(ctx, key.ID)
	require.ErrorIs(t, err, modelgateway.ErrNotFound)
	require.ErrorContains(t, err, "no stored value")
	var storedVersion *string
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT secret_version_id FROM gateway_keys WHERE id = ?", key.ID).Scan(&storedVersion))
	assert.Nil(t, storedVersion)

	require.NoError(t, cli.gatewayCommand(ctx, []string{"keys", "set", key.ID, "--endpoints", "*"}))
	var storedEndpoints *string
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT endpoints FROM gateway_keys WHERE id = ?", key.ID).Scan(&storedEndpoints))
	assert.Nil(t, storedEndpoints)
}
