package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #562): the owner limits a Bot to some dashboards of a
// Superset Connection. A scope is of a Superset Connection that exists, each
// dashboard a number named once; it is kept and read back, and taken away by
// leaving it out. A Bot without one has the revision it had before scopes
// were a setting, so an undo recorded then is not made stale.
func TestABotsDashboardScopeIsChecked(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.SaveServiceConnection(ctx, Connection{Integration: "superset", Label: "prod", Secret: "s-prod"})
	require.NoError(t, err)

	plain, err := w.s.PutBot(ctx, Bot{Name: "plain"})
	require.NoError(t, err)
	assert.NotContains(t, botRevision(plain), "scopes")

	for _, c := range []struct {
		scopes map[string]ConnectionScope
		want   string
	}{
		{map[string]ConnectionScope{"google:work": {Dashboards: []int64{1}}}, "a scope is of a superset connection"},
		{map[string]ConnectionScope{"superset:prod": {Dashboards: []int64{8, 8}}}, "named twice"},
		{map[string]ConnectionScope{"superset:prod": {Dashboards: []int64{0}}}, "not a dashboard's number"},
		{map[string]ConnectionScope{"superset:gone": {Dashboards: []int64{8}}}, "has no connection gone"},
	} {
		_, err := w.s.PutBot(ctx, Bot{Name: "community", Scopes: c.scopes})
		require.ErrorContains(t, err, c.want)
	}

	community, err := w.s.PutBot(ctx, Bot{Name: "community",
		Scopes: map[string]ConnectionScope{"superset:prod": {Dashboards: []int64{8}}}})
	require.NoError(t, err)

	read, err := w.s.GetBot(ctx, community.ID)
	require.NoError(t, err)

	scope, limited := read.ScopeOf("superset:prod")
	assert.True(t, limited)
	assert.Equal(t, []int64{8}, scope.Dashboards)

	_, limited = read.ScopeOf("superset:other")
	assert.False(t, limited, "a Connection without a scope is reached whole")

	read.Scopes = nil
	read, err = w.s.PutBot(ctx, read)
	require.NoError(t, err)
	assert.Nil(t, read.Scopes)
}
