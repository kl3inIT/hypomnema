package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
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

// putVersion makes a new current version of a profile in a database at an
// earlier migration, by its columns of that time: the Service's queries read
// the columns of the newest one.
func putVersion(ctx context.Context, t *testing.T, db *sql.DB, id, profile, source, provider, model, effort string) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort,
		                              description, created_at)
		VALUES (?, ?, (SELECT coalesce(max(version), 0) + 1 FROM profile_versions WHERE profile = ?), ?, ?, 'claude',
		        ?, ?, nullif(?, ''), 'd', 100);
		UPDATE profiles SET current_version_id = ? WHERE name = ?;`,
		id, profile, profile, source, id, provider, model, effort, id, profile)
	require.NoError(t, err)
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

// migratedTo is a fresh database migrated up to version, with a Service over
// it.
func migratedTo(t *testing.T, version int64) (*Service, *sql.DB) {
	t.Helper()

	db, _ := databaseAt(t, version)

	n := 0

	return New(db, &fakeClock{now: t0}, func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}), db
}

// Scenario: a fresh install (issues #462, #549). Migrated to the top, its
// starting profiles name the Claude subscription, in the versions 00102, 00111 and 00115 made,
// and the gateway's table holds no seeded model nor the provider
// model-gateway; so a Task that names no profile is placed as a Claude Code
// worker through the gateway under the subscription's model, with no profile
// applied and no account on the node, and its offer says to speak Anthropic
// Messages; once a Claude account is logged in on the control plane, not before.
func TestAFreshInstallRunsClaudeCodeThroughTheGateway(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()

	_, err := w.s.Profiles().GetProvider(ctx, "model-gateway")
	require.ErrorIs(t, err, ErrNotFound, "named by nothing once the profiles moved")
	assert.Empty(t, gatewayModelsOf(ctx, t, w.db), "the subscription's models need no row")

	assert.Equal(t, map[string]string{
		"implementer": "pfv_seed4_implementer", "reviewer": "pfv_seed8_reviewer", "explorer": "pfv_seed5_explorer",
	}, currentVersions(ctx, t, w.db))

	reviewer, err := w.s.Profiles().GetProfile(ctx, "reviewer")
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-subscription", "claude-opus-5-5"}, []string{reviewer.Provider, reviewer.Model})
	assert.Contains(t, reviewer.Source, "  provider: claude-subscription\n", "its YAML says so too")
	assert.Equal(t, "high", reviewer.Effort, "the reviewer keeps its effort")
	assert.Contains(t, reviewer.Instructions, "Verdict: `correct` or `incorrect`", "and its instruction")
	assert.Equal(t, &profilestore.Availability{Reason: ReasonNoAccount}, reviewer.Availability,
		"no Claude account is logged in on the control plane yet")

	w.tasks(NewTask{Ref: "a", Spec: "a"})
	require.Empty(t, w.schedule().Created, "nothing can serve the model yet")

	w.gatewayAccount("claude")

	reviewer, err = w.s.Profiles().GetProfile(ctx, "reviewer")
	require.NoError(t, err)
	assert.Equal(t, &profilestore.Availability{Available: true}, reviewer.Availability, "node_a has Claude Code")

	created := w.schedule().Created
	require.Len(t, created, 1, "placed with no profile applied")

	d := w.dispatchOf(created[0])
	assert.Equal(t, w.node.ID, d.NodeID)
	assert.Equal(t, "pfv_seed4_implementer", d.ProfileVersionID)
	assert.Equal(t, "claude-subscription/claude-opus-5-5", d.Model, "the gateway's name for the subscription's model")

	conv := w.conversation(d.ConversationID)
	assert.Equal(t, "claude", conv.Runtime)
	assert.Equal(t, profilestore.GatewayAccountRef, conv.AccountRef, "through the gateway, under no account of the node")

	pending, err := w.s.NodePending(ctx, w.node.ID)
	require.NoError(t, err)
	require.Len(t, pending.Offers, 1)
	assert.Equal(t, "anthropic-messages", pending.Offers[0].ModelAPI, "the Claude subscription's own wire")
}

// Scenario: an install where the owner made the gateway by hand, as zm
// (issue #462): a model_gateway provider of another name, the three gateway
// models, and an implementer of the owner's. Migrating through 00088 leaves
// all of it as it was; with no provider model-gateway, the profiles the owner
// has not changed stay on their seeds too. (00090 then moves them to the
// subscription: TestMigrationMovesGatewayModelsToTheirSubscription.)
func TestMigrationLeavesAnOwnersGatewayAndProfiles(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, p := databaseAt(t, 80)

	_, err := db.ExecContext(ctx, `
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

	_, err = p.UpTo(ctx, 88)
	require.NoError(t, err)

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
// profile of its own on the provider, before 00090 moved the seeds off it,
// the provider and the model it uses stay.
func TestMigrationDownTakesTheGatewaySeedBack(t *testing.T) {
	t.Parallel()

	for _, owned := range []bool{false, true} {
		_, db := migratedTo(t, 88)
		ctx := t.Context()

		const custom = "pfv_owner_explorer"

		if owned {
			putVersion(ctx, t, db, custom, "explorer",
				"name: explorer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway, name: claude-haiku-5-5}\n",
				"model-gateway", "claude-haiku-5-5", "")
		}

		p, err := provider(db)
		require.NoError(t, err)
		_, err = p.DownTo(ctx, 80)
		require.NoError(t, err)

		var providers int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT count(*) FROM providers WHERE name = 'model-gateway'`).Scan(&providers))

		if !owned {
			assert.Zero(t, providers, "made here and named by nothing")
			assert.Empty(t, gatewayModelsOf(ctx, t, db))
			assert.Equal(t, map[string]string{
				"implementer": "pfv_seed2_implementer", "reviewer": "pfv_seed3_reviewer", "explorer": "pfv_seed3_explorer",
			}, currentVersions(ctx, t, db))

			continue
		}

		assert.Equal(t, 1, providers, "the owner's explorer names it")
		assert.Equal(t, []string{"claude-haiku-5-5"}, slices.Sorted(maps.Keys(gatewayModelsOf(ctx, t, db))),
			"the owner's explorer uses it")
		assert.Equal(t, map[string]string{
			"implementer": "pfv_seed2_implementer", "reviewer": "pfv_seed3_reviewer", "explorer": custom,
		}, currentVersions(ctx, t, db))
	}
}

// Scenario: an install at 00089 whose Bot, a chat and two profiles run on the
// provider model-gateway (issue #549). The Bot's, the chat's and the
// explorer's gateway models are served from one subscription model each, the
// explorer's YAML naming its keys in another order; the reviewer's, duo, is
// the owner's name over two targets. Migrating moves each single one to its
// subscription and model, YAML included; duo and the reviewer stay, as a name
// over several targets is made on purpose for its failover, and so does the
// provider they name; the single gateway models nothing uses go.
func TestMigrationMovesGatewayModelsToTheirSubscription(t *testing.T) {
	t.Parallel()

	_, db := migratedTo(t, 89)
	ctx := t.Context()

	_, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO models (provider, name)
		VALUES ('model-gateway', 'claude-opus-5-5'), ('model-gateway', 'claude-sonnet-5-5'),
		       ('model-gateway', 'claude-haiku-5-5'), ('model-gateway', 'duo');
		INSERT INTO gateway_models (name, capability, source, created_at, updated_at)
		VALUES ('duo', 'chat', 'name: duo', 100, 100);
		INSERT INTO gateway_model_targets (model, position, provider, upstream_model)
		VALUES ('duo', 0, 'claude-subscription', 'claude-opus-5-5'),
		       ('duo', 1, 'claude-subscription', 'claude-sonnet-5-5');
		UPDATE bots SET provider = 'model-gateway', model = 'claude-sonnet-5-5' WHERE id = ?;
		INSERT INTO chats (id, title, bot_id, provider, model, created_at, updated_at)
		VALUES ('cht_1', 'hi', ?, 'model-gateway', 'claude-haiku-5-5', 100, 100);`, DefaultBotID, DefaultBotID)
	require.NoError(t, err)

	const reviewer, explorer = "pfv_owner_reviewer", "pfv_owner_explorer"

	putVersion(ctx, t, db, reviewer, "reviewer",
		"name: reviewer\ndescription: d\nruntime: claude\nmodel: {provider: model-gateway, name: duo}\n",
		"model-gateway", "duo", "")
	putVersion(ctx, t, db, explorer, "explorer", "name: explorer\ndescription: d\nruntime: claude\n"+
		"model: {name: claude-sonnet-5-5, provider: model-gateway, effort: high}\n",
		"model-gateway", "claude-sonnet-5-5", "high")

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 90)
	require.NoError(t, err)

	read := func(query string, args ...any) [2]string {
		t.Helper()

		var a, b string
		require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&a, &b))

		return [2]string{a, b}
	}

	assert.Equal(t, [2]string{"claude-subscription", "claude-sonnet-5-5"},
		read(`SELECT provider, model FROM bots WHERE id = ?`, DefaultBotID))
	assert.Equal(t, [2]string{"claude-subscription", "claude-haiku-5-5"},
		read(`SELECT provider, model FROM chats WHERE id = 'cht_1'`))
	assert.Equal(t, [2]string{"model-gateway", "duo"},
		read(`SELECT provider, model FROM profile_versions WHERE id = ?`, reviewer),
		"a name over two targets keeps its failover")
	assert.Equal(t, [2]string{"claude-subscription", "claude-sonnet-5-5"},
		read(`SELECT provider, model FROM profile_versions WHERE id = ?`, explorer), "in the same version")
	assert.Contains(t, read(`SELECT source, '' FROM profile_versions WHERE id = ?`, explorer)[0],
		"model: {name: claude-sonnet-5-5, provider: claude-subscription, effort: high}", "its YAML too")
	assert.Equal(t, [2]string{"claude-subscription", "claude-opus-5-5"},
		read(`SELECT provider, model FROM profile_versions WHERE id = 'pfv_seed3_implementer'`), "a seed too")

	assert.Equal(t, []string{"duo"}, slices.Sorted(maps.Keys(gatewayModelsOf(ctx, t, db))),
		"a name over two targets stays; the single ones nothing uses go")

	var providers int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM providers WHERE name = 'model-gateway'`).Scan(&providers))
	assert.Equal(t, 1, providers, "the reviewer still names it")
}
