package platform_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

func envOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// A misconfigured process must name every bad setting in one start, not one
// per restart.
func TestEnvReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()

	env := platform.NewEnv(envOf(map[string]string{
		"X_LOG_FORMAT": "xml",
	}))

	env.Required("X_URL")
	env.Log("X")

	err := env.Err()
	require.Error(t, err)
	require.ErrorIs(t, err, platform.ErrMissing)
	require.ErrorContains(t, err, "X_URL")
	require.ErrorContains(t, err, "X_LOG_FORMAT")
}

func TestEnvDefaults(t *testing.T) {
	t.Parallel()

	env := platform.NewEnv(envOf(map[string]string{"X_ADDR": " "}))

	assert.Equal(t, "fallback", env.String("X_ADDR", "fallback"))
	assert.Equal(t, platform.LogConfig{Level: slog.LevelInfo, Format: platform.LogFormatJSON}, env.Log("X"))
	require.NoError(t, env.Err())
}
