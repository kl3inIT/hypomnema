package orchestration

import (
	"context"
	"database/sql"
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// gatewayModelsOf lists the gateway's models as name, first target and
// source, by name.
func gatewayModelsOf(ctx context.Context, t *testing.T, db *sql.DB) map[string][2]string {
	t.Helper()

	rows, err := db.QueryContext(ctx, `
		SELECT m.name, t.provider || ':' || t.upstream_model, m.source
		FROM gateway_models m JOIN gateway_model_targets t ON t.model = m.name AND t.position = 0`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	out := map[string][2]string{}

	for rows.Next() {
		var name, target, source string
		require.NoError(t, rows.Scan(&name, &target, &source))
		out[name] = [2]string{target, source}
	}

	require.NoError(t, rows.Err())

	return out
}

// currentVersions maps each profile to the id of its current version.
func currentVersions(ctx context.Context, t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	rows, err := db.QueryContext(ctx, `SELECT name, current_version_id FROM profiles`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	out := map[string]string{}

	for rows.Next() {
		var name, id string
		require.NoError(t, rows.Scan(&name, &id))
		out[name] = id
	}

	require.NoError(t, rows.Err())

	return out
}

// Scenario: a fresh install (issue #462). Migrated to the top, it has the
// provider model-gateway with three Claude models, gateway models of those
// names served from the Claude subscription, and its starting profiles on
// the gateway; so a Task that names no profile is placed as a Claude Code
// worker through the gateway, with no profile applied and no account on the
// node.
func TestAFreshInstallRunsClaudeCodeThroughTheGateway(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	gateway, err := w.s.GetProvider(ctx, "model-gateway")
	require.NoError(t, err)
	assert.Equal(t, ProviderModelGateway, gateway.Kind)
	assert.ElementsMatch(t, []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, gateway.Models)

	models := gatewayModelsOf(ctx, t, w.db)
	assert.Equal(t, map[string][2]string{
		"claude-opus-5-5": {"claude-subscription:claude-opus-5-5",
			"name: claude-opus-5-5\ntargets:\n  - {provider: claude-subscription, model: claude-opus-5-5}\n"},
		"claude-sonnet-5-5": {"claude-subscription:claude-sonnet-5-5",
			"name: claude-sonnet-5-5\ntargets:\n  - {provider: claude-subscription, model: claude-sonnet-5-5}\n"},
		"claude-haiku-5-5": {"claude-subscription:claude-haiku-5-5",
			"name: claude-haiku-5-5\ntargets:\n  - {provider: claude-subscription, model: claude-haiku-5-5}\n"},
	}, models)

	assert.Equal(t, map[string]string{
		"implementer": "pfv_seed3_implementer", "reviewer": "pfv_seed4_reviewer", "explorer": "pfv_seed4_explorer",
	}, currentVersions(ctx, t, w.db))

	reviewer, err := w.s.GetProfile(ctx, "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "high", reviewer.Effort, "the reviewer keeps its effort")
	assert.Contains(t, reviewer.Instruction, "Verdict: `correct` or `incorrect`", "and its instruction")
	assert.Equal(t, &Availability{Available: true}, reviewer.Availability, "node_a has Claude Code")

	w.tasks(NewTask{Ref: "a", Spec: "a"})

	created := w.schedule().Created
	require.Len(t, created, 1, "placed with no profile applied")

	d := w.dispatchOf(created[0])
	assert.Equal(t, w.node.ID, d.NodeID)
	assert.Equal(t, "pfv_seed3_implementer", d.ProfileVersionID)
	assert.Equal(t, "claude-opus-5-5", d.Model)

	conv := w.conversation(d.ConversationID)
	assert.Equal(t, "claude", conv.Runtime)
	assert.Equal(t, GatewayAccountRef, conv.AccountRef, "through the gateway, under no account of the node")
}

// Scenario: an install where the owner made the gateway by hand, as zm
// (issue #462): a model_gateway provider of another name, the three gateway
// models, and an implementer of the owner's. Migrating to the top leaves all
// of it as it was; with no provider model-gateway, the profiles the owner
// has not changed stay on their seeds too.
func TestMigrationLeavesAnOwnersGatewayAndProfiles(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 80)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO providers (name, kind, source, created_at, updated_at)
		VALUES ('gw', 'model_gateway', 'name: gw', 100, 100);
		INSERT INTO models (provider, name) VALUES ('gw', 'claude-opus-5-5'), ('gw', 'claude-sonnet-5-5');
		INSERT INTO gateway_models (name, capability, source, created_at, updated_at)
		VALUES ('claude-opus-5-5', 'chat', 'name: claude-opus-5-5 # the owner''s', 100, 100),
		       ('claude-sonnet-5-5', 'chat', 'name: claude-sonnet-5-5 # the owner''s', 100, 100),
		       ('claude-haiku-5-5', 'chat', 'name: claude-haiku-5-5 # the owner''s', 100, 100);
		INSERT INTO gateway_model_targets (model, position, provider, upstream_model)
		VALUES ('claude-opus-5-5', 0, 'claude-subscription', 'claude-opus-5-5'),
		       ('claude-sonnet-5-5', 0, 'claude-subscription', 'claude-sonnet-5-5'),
		       ('claude-haiku-5-5', 0, 'claude-subscription', 'claude-haiku-5-5');
		INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, description,
		                              created_at)
		VALUES ('pfv_owner', 'implementer', 3, 'name: implementer', 'd', 'claude', 'gw', 'claude-opus-5-5', 'd', 100);
		UPDATE profiles SET current_version_id = 'pfv_owner' WHERE name = 'implementer';`)
	require.NoError(t, err)

	before := gatewayModelsOf(ctx, t, db)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	var made int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM providers WHERE name = 'model-gateway'`).Scan(&made))
	assert.Zero(t, made, "the owner's gateway is the one")

	assert.Equal(t, before, gatewayModelsOf(ctx, t, db))
	assert.Equal(t, map[string]string{
		"implementer": "pfv_owner", "reviewer": "pfv_seed3_reviewer", "explorer": "pfv_seed3_explorer",
	}, currentVersions(ctx, t, db))
}

// Scenario: the way back (issue #462). On a fresh install the seed versions
// are taken back and the provider and gateway models go; once the owner put a
// profile of its own on the provider, the provider and the model it uses stay.
func TestMigrationDownTakesTheGatewaySeedBack(t *testing.T) {
	t.Parallel()

	for _, owned := range []bool{false, true} {
		w := newSeedWorld(t)
		ctx := t.Context()

		var custom Profile

		if owned {
			var err error

			custom, _, err = w.s.ApplyProfile(ctx, "",
				"name: explorer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway, name: claude-haiku-5-5}\n")
			require.NoError(t, err)
		}

		p, err := provider(w.db)
		require.NoError(t, err)
		_, err = p.DownTo(ctx, 80)
		require.NoError(t, err)

		var providers int
		require.NoError(t, w.db.QueryRowContext(ctx,
			`SELECT count(*) FROM providers WHERE name = 'model-gateway'`).Scan(&providers))

		if !owned {
			assert.Zero(t, providers, "made here and named by nothing")
			assert.Empty(t, gatewayModelsOf(ctx, t, w.db))
			assert.Equal(t, map[string]string{
				"implementer": "pfv_seed2_implementer", "reviewer": "pfv_seed3_reviewer", "explorer": "pfv_seed3_explorer",
			}, currentVersions(ctx, t, w.db))

			continue
		}

		assert.Equal(t, 1, providers, "the owner's explorer names it")
		assert.Equal(t, []string{"claude-haiku-5-5"}, slices.Sorted(maps.Keys(gatewayModelsOf(ctx, t, w.db))),
			"the owner's explorer uses it")
		assert.Equal(t, map[string]string{
			"implementer": "pfv_seed2_implementer", "reviewer": "pfv_seed3_reviewer", "explorer": custom.VersionID,
		}, currentVersions(ctx, t, w.db))
	}
}
