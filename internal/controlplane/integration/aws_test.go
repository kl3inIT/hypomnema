package integration_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration/ststest"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	readRole     = "arn:aws:iam::123456789012:role/hypomnema/read"
	machinesRole = "arn:aws:iam::123456789012:role/hypomnema/machines"
	keyID        = "AKIAEXAMPLEKEY00001"
	newKeyID     = "AKIAEXAMPLEKEY00002"
)

func awsWorld(t *testing.T) (*world, *ststest.STS) {
	t.Helper()

	w := newWorld(t)
	f := ststest.New(t, map[string]string{readRole: keyID, machinesRole: keyID})
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.AWS()).WithSTS(f.URL)

	return w, f
}

// Scenario: the owner gives an IAM user's key once, with the role that reads
// his infrastructure (issue #331). A role the key may not assume is refused
// and nothing is kept. The role that works is a Connection naming its AWS
// account and ARN, the key is sealed in the vault, and a second role is
// connected with the same key by its name alone. A role's session is assumed
// for 12 hours, kept, and assumed again only when it is about to end.
func TestTheOwnerConnectsRolesOfHisAWSAccountWithOneKey(t *testing.T) {
	t.Parallel()

	w, f := awsWorld(t)
	ctx := t.Context()

	//nolint:gosec // A made-up key of a test.
	_, err := w.svc.ConnectRole(ctx, integration.AWSID, "main-read", integration.Role{
		ARN: readRole, Key: "main", AccessKeyID: newKeyID, SecretAccessKey: "a-key-the-role-does-not-trust",
	})
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.Contains(t, err.Error(), "AccessDenied")
	assert.NotContains(t, err.Error(), "a-key-the-role-does-not-trust")

	secrets, err := w.vault.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, secrets, "a refused role keeps no key")

	read, err := w.svc.ConnectRole(ctx, integration.AWSID, "main-read", integration.Role{
		ARN: readRole, Key: "main", AccessKeyID: keyID, SecretAccessKey: "the-secret-access-key",
	})
	require.NoError(t, err)
	assert.Equal(t, "123456789012", read.Account)
	assert.Equal(t, readRole, read.RoleARN)
	assert.Equal(t, "main", read.Key)

	machines, err := w.svc.ConnectRole(ctx, integration.AWSID, "main-machines", integration.Role{
		ARN: machinesRole, Key: "main",
	})
	require.NoError(t, err, "a second role is assumed with the key kept")
	assert.Equal(t, machinesRole, machines.RoleARN)

	secrets, err = w.vault.List(ctx)
	require.NoError(t, err)
	require.Len(t, secrets, 1, "one key for both roles")
	assert.Equal(t, "integration-aws-main", secrets[0].Name)

	_, err = w.svc.ConnectRole(ctx, integration.AWSID, "other", integration.Role{ARN: readRole, Key: "nothing-kept"})
	require.ErrorIs(t, err, integration.ErrInvalid, "a key not kept must be given")

	creds, err := w.svc.RoleCredentials(ctx, integration.AWSID, "main-read")
	require.NoError(t, err)
	assert.Equal(t, "session-token", creds.SessionToken)

	_, err = w.svc.RoleCredentials(ctx, integration.AWSID, "main-read")
	require.NoError(t, err)

	assert.Equal(t, []string{
		readRole + " by " + newKeyID + " for 43200s as hypomnema",
		readRole + " by " + keyID + " for 43200s as hypomnema",
		machinesRole + " by " + keyID + " for 43200s as hypomnema",
	}, f.Assumed(), "a session is kept until it is about to end")

	// A session about to end is assumed again before it is used.
	f.Lasts(10 * time.Second)

	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.AWS()).WithSTS(f.URL)

	for range 2 {
		_, err = w.svc.RoleCredentials(ctx, integration.AWSID, "main-read")
		require.NoError(t, err)
	}

	assert.Len(t, f.Assumed(), 5, "a restarted control plane assumes the role from the key in the vault, and a "+
		"session that ends within 30 s is assumed again")
}

// Scenario: AWS stops letting the key assume a role (the role's trust was
// changed). Asking for the role's session says so and marks the Connection
// as needing to be connected again; the owner gives a new key under the same
// name, and every role assumed with the old one is assumed with the new.
// Removing one role keeps the key while another uses it; removing the last
// removes the key.
func TestARoleAWSStopsTrustingAsksForAnotherKey(t *testing.T) {
	t.Parallel()

	w, f := awsWorld(t)
	ctx := t.Context()

	// Sessions that end at once, so that each use assumes the role again; one already given works until it ends.
	f.Lasts(10 * time.Second)

	for label, arn := range map[string]string{"main-read": readRole, "main-machines": machinesRole} {
		_, err := w.svc.ConnectRole(ctx, integration.AWSID, label, integration.Role{
			ARN: arn, Key: "main", AccessKeyID: keyID, SecretAccessKey: "the-secret-access-key",
		})
		require.NoError(t, err)
	}

	f.Trust(map[string]string{readRole: newKeyID, machinesRole: newKeyID})

	_, err := w.svc.RoleCredentials(ctx, integration.AWSID, "main-read")
	require.ErrorIs(t, err, integration.ErrSignInRequired)
	assert.True(t, w.connection("main-read").SignInRequired)

	_, err = w.svc.ConnectRole(ctx, integration.AWSID, "main-read", integration.Role{
		ARN: readRole, Key: "main", AccessKeyID: newKeyID, SecretAccessKey: "the-new-secret",
	})
	require.NoError(t, err)

	_, err = w.svc.RoleCredentials(ctx, integration.AWSID, "main-machines")
	require.NoError(t, err, "the other role is assumed with the new key")
	assert.False(t, w.connection("main-read").SignInRequired)

	_, err = w.svc.Disconnect(ctx, integration.AWSID, "main-read")
	require.NoError(t, err)

	secrets, err := w.vault.List(ctx)
	require.NoError(t, err)
	assert.Len(t, secrets, 1, "the key stays while a role uses it")

	_, err = w.svc.Disconnect(ctx, integration.AWSID, "main-machines")
	require.NoError(t, err)

	secrets, err = w.vault.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, secrets, "the last role takes the key with it")
}

func (w *world) connection(label string) integration.Connection {
	w.t.Helper()

	all, err := w.svc.List(w.t.Context())
	require.NoError(w.t, err)

	for _, in := range all {
		for _, c := range in.Connections {
			if c.Label == label {
				return c
			}
		}
	}

	w.t.Fatalf("no connection %s", label)

	return integration.Connection{}
}
