package clientcli

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration/ststest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

const testRole = "arn:aws:iam::123456789012:role/hypomnema/read"

// Scenario: the owner connects the role agents read his AWS account with
// (issue #331), giving the access key file the AWS console downloads. The
// key is never printed; the answer names the role, its account and the
// key's name. A second role is connected with the key's name alone, and
// integrations list shows both.
func TestTheOwnerConnectsAnAWSRoleWithTheConsolesKeyFile(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, keys,
		func(ctx context.Context, ref string) ([]string, error) {
			name, _ := vault.NameFromRef(ref)

			return svc.ConnectionsUsingSecret(ctx, name)
		})

	sts := ststest.New(t, map[string]string{testRole: "AKIAEXAMPLEKEY00001",
		"arn:aws:iam::123456789012:role/hypomnema/machines": "AKIAEXAMPLEKEY00001"})
	integrations := integration.New(svc, secrets, platform.SystemClock{}, platform.NewID, integration.AWS()).
		WithSTS(sts.URL)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, slog.New(slog.DiscardHandler), stop).WithIntegrations(integrations).
		Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	w := &world{svc: svc, url: hs.URL, home: filepath.Join(t.TempDir(), "home")}
	w.login(t, bearertoken.Owner)

	file := filepath.Join(t.TempDir(), "hypomnema_accessKeys.csv")
	require.NoError(t, os.WriteFile(file,
		[]byte(string(rune(0xFEFF))+"Access key ID,Secret access key\r\n"+
			"AKIAEXAMPLEKEY00001,the-secret-access-key\r\n"), 0o600))

	out, err := w.cmd(t, "integrations", "connect", "aws", "--label", "main-read", "--role", testRole,
		"--key", "main", "--key-file", file)
	require.NoError(t, err, out)
	assert.Equal(t, "aws:main-read is the role "+testRole+" of account 123456789012, assumed with key main\n", out)

	out, err = w.cmd(t, "integrations", "connect", "aws", "--label", "main-machines", "--role",
		"arn:aws:iam::123456789012:role/hypomnema/machines")
	require.NoError(t, err, out)

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "main-read")
	assert.Contains(t, out, "main-machines")
	assert.NotContains(t, out, "the-secret-access-key")

	_, err = w.cmd(t, "integrations", "connect", "aws", "--label", "x")
	require.Error(t, err, "a role is named by its ARN")

	// The role may do all its policies allow; the owner has one operation, named as AWS names it, ask first.
	out, err = w.cmd(t, "integrations", "actions", "aws:main-read")
	require.NoError(t, err)
	assert.Contains(t, out, "agents may do whatever the role allows")

	out, err = w.cmd(t, "integrations", "actions", "set", "aws:main-read", "aws.ec2.RebootInstances", "ask")
	require.NoError(t, err, out)
	assert.Contains(t, out, "aws.ec2.RebootInstances ask")

	out, err = w.cmd(t, "integrations", "actions", "aws:main-read")
	require.NoError(t, err)
	assert.Contains(t, out, "aws.ec2.RebootInstances ask")
}
