package controlplane

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/machines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario: the owner sets up AWS as a machine provider. Its key must
// already be in the vault and its options must be ones AWS takes; once it is
// added, the vault refuses to delete the key it names, and a machine cannot
// be asked for while the control plane has no public URL to give it.
func TestOwnerSetsUpAMachineProvider(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	cfg := vaultConfig(t, path, "k1", keygen(t, "k1"))
	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	add := []string{
		"machines", "providers", "add", "aws", "--kind", "aws", "--key", "vault:aws", "--region", "ap-southeast-1",
		"--size", "t3.medium", "--max", "2", "--option", "disk_gib=80",
	}

	_, err := command(t, cfg, add...)
	require.ErrorContains(t, err, "the key of the machine provider")

	file := filepath.Join(t.TempDir(), "aws.key")
	require.NoError(t, os.WriteFile(file, []byte("AKIAEXAMPLE:not-a-real-secret\n"), 0o600))

	_, err = command(t, cfg, "secrets", "add", "aws", "--from-file", file)
	require.NoError(t, err)

	_, err = command(t, cfg, append(add, "--option", "vpc=vpc-1")...)
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	out, err := command(t, cfg, add...)
	require.NoError(t, err)
	assert.Contains(t, out, "aws, ap-southeast-1 t3.medium, up to 2 machines")
	assert.NotContains(t, out, "not-a-real-secret")

	out, err = command(t, cfg, "machines", "providers", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "\taws\taws\tap-southeast-1\tt3.medium\tup to 2\tdisk_gib=80")

	_, err = command(t, cfg, "secrets", "rm", "aws")
	require.ErrorContains(t, err, "machine provider aws")

	_, err = command(t, cfg, "machines", "create", "build-1", "--provider", "aws")
	require.ErrorIs(t, err, machines.ErrNoPublicURL)

	out, err = command(t, cfg, "nodes", "list")
	require.NoError(t, err)
	assert.Empty(t, out)

	_, err = command(t, cfg, "machines", "providers", "rm", "aws")
	require.NoError(t, err)

	_, err = command(t, cfg, "secrets", "rm", "aws")
	require.NoError(t, err)

	_, err = command(t, cfg, "machines", "teleport")
	require.ErrorIs(t, err, ErrUsage)
}
