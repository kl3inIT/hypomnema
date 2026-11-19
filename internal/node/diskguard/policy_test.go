package diskguard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiskPolicyValidation(t *testing.T) {
	t.Parallel()
	defaults := DefaultPolicy()
	assert.Equal(t, Policy{
		FloorBytes: 5 << 30, TrimPercent: 15, TargetPercent: 20, GoCacheMaxBytes: 8 << 30, TempMaxAgeHours: 6,
	}, defaults)
	require.NoError(t, defaults.Validate())
	invalidCap := defaults
	invalidCap.GoCacheMaxBytes = 0
	require.Error(t, invalidCap.Validate())
	invalidAge := defaults
	invalidAge.TempMaxAgeHours = -1
	require.Error(t, invalidAge.Validate())

	for _, policy := range []Policy{
		{FloorBytes: 0, TrimPercent: 15, TargetPercent: 20},
		{FloorBytes: 1, TrimPercent: 0, TargetPercent: 20},
		{FloorBytes: 1, TrimPercent: 20, TargetPercent: 20},
		{FloorBytes: 1, TrimPercent: 20, TargetPercent: 15},
		{FloorBytes: 1, TrimPercent: 15, TargetPercent: 101},
	} {
		assert.Error(t, policy.Validate())
	}
}
