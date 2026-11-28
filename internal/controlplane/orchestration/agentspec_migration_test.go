package orchestration

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mixedSource is the YAML of a profile whose backups the migration converts, with the instructions under their old
// name.
const mixedSource = "name: mixed\ndescription: d\nruntime: codex\n" +
	"model: {provider: codex-subscription, name: gpt-6.2-sol}\ninstruction: Work carefully.\n" +
	"fallbacks: [explorer, plain, codex-worker, missing, codex-reviewer, implementer-codex]\n"

// Scenario (issue #1336), on data shaped as a live control plane's of 2026-11-28 (its version numbers raised above
// the seeds'): the implementer and the reviewer once named backup profiles (codex-worker, then implementer-codex;
// codex-reviewer), now deleted, and their current versions are roles with no fallbacks; every Bot has a
// provider/model list. The migration gives each version that named backups their models in order, drops what has no
// model to give (a role, a backup that ran its runtime's default, a name no profile has, a model named twice or the
// version's own), renames persona and instruction to instructions, and leaves the Bots' fallbacks as they are. Going
// down puts back the names.
func TestTheMigrationTurnsBackupProfilesIntoTheirModels(t *testing.T) {
	t.Parallel()

	own := migrationVersion(t, "_agent_spec.sql")
	svc, db := migratedTo(t, own-1)
	ctx := t.Context()

	exec := func(query string, args ...any) {
		t.Helper()

		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}

	version := func(id, profile string, n int, runtime, provider, model, effort, fallbacks string) {
		t.Helper()
		exec(`INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort,
				instruction, fallbacks, description, created_at)
			VALUES (?, ?, ?, 'name: '||?, 'd', ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), 'Work carefully.', ?,
				'd', 100)`, id, profile, n, profile, runtime, provider, model, effort, fallbacks)
	}

	profile := func(name, current string, deleted bool) {
		t.Helper()

		var at sql.NullInt64
		if deleted {
			at = sql.NullInt64{Int64: 200, Valid: true}
		}

		exec(`INSERT INTO profiles (name, current_version_id, created_at, updated_at, deleted_at)
			VALUES (?, ?, 1, 1, ?)
			ON CONFLICT (name) DO UPDATE SET current_version_id = excluded.current_version_id,
				deleted_at = excluded.deleted_at`, name, current, at)
	}

	for _, p := range []struct{ name, id, runtime, provider, model, effort string }{
		{"codex-worker", "pfv_cw", "codex", "codex-subscription", "gpt-6.1-sol", ""},
		{"implementer-codex", "pfv_ic", "codex", "codex-subscription", "gpt-6.2-sol", ""},
		{"codex-reviewer", "pfv_cr", "codex", "codex-subscription", "gpt-6.1-sol", "high"},
		{"plain", "pfv_plain", "codex", "", "", ""},
	} {
		profile(p.name, p.id, p.name != "plain")
		version(p.id, p.name, 1, p.runtime, p.provider, p.model, p.effort, "")
	}

	profile("implementer", "pfv_im7", false)
	version("pfv_im3", "implementer", 103, "claude", "", "", "", "codex-worker")
	version("pfv_im4", "implementer", 104, "claude", "claude-subscription", "claude-opus-5-5", "", "codex-worker")
	version("pfv_im6", "implementer", 106, "claude", "claude-subscription", "claude-opus-5-5", "", "implementer-codex")
	version("pfv_im7", "implementer", 107, "@role", "", "", "", "")

	profile("reviewer", "pfv_rv5", false)
	version("pfv_rv5", "reviewer", 105, "claude", "claude-subscription", "claude-opus-5-5", "high", "codex-reviewer")

	// What no reference shape has, for the rules: a role, a backup with no model, a name no profile has, a model
	// named twice and the version's own model.
	profile("mixed", "pfv_mixed", false)
	profile("explorer", "pfv_ex", false)
	version("pfv_ex", "explorer", 101, "@role", "", "", "", "")
	version("pfv_mixed", "mixed", 1, "codex", "codex-subscription", "gpt-6.2-sol", "",
		"explorer,plain,codex-worker,missing,codex-reviewer,implementer-codex")

	exec(`UPDATE profile_versions SET source = ? WHERE id = 'pfv_mixed'`, mixedSource)
	exec(`INSERT OR IGNORE INTO models (provider, name) VALUES ('codex-subscription', 'gpt-6.1-sol'),
		('codex-subscription', 'gpt-6.2-sol')`)

	exec(`INSERT INTO bots (id, name, provider, model, persona, fallbacks, created_at, updated_at)
		VALUES ('bot_maps', 'maps', 'codex-subscription', 'gpt-6.2-sol', 'You answer about maps.',
			'claude-subscription/claude-sonnet-5-5,opencode-go/deepseek-v4.1-flash', 1, 1)`)

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, own)
	require.NoError(t, err)

	fallbacks := func() map[string]string {
		rows, err := db.QueryContext(ctx, `SELECT id, fallbacks FROM profile_versions WHERE id LIKE 'pfv_%'`)
		require.NoError(t, err)

		defer func() { _ = rows.Close() }()

		out := map[string]string{}
		for rows.Next() {
			var id, list string
			require.NoError(t, rows.Scan(&id, &list))
			out[id] = list
		}

		require.NoError(t, rows.Err())

		return out
	}

	got := fallbacks()
	assert.Equal(t, "codex-subscription/gpt-6.1-sol", got["pfv_im3"], "a deleted backup's model")
	assert.Equal(t, "codex-subscription/gpt-6.1-sol", got["pfv_im4"])
	assert.Equal(t, "codex-subscription/gpt-6.2-sol", got["pfv_im6"])
	assert.Empty(t, got["pfv_im7"], "the role version had none")
	assert.Equal(t, "codex-subscription/gpt-6.1-sol", got["pfv_rv5"])
	assert.Equal(t, "codex-subscription/gpt-6.1-sol", got["pfv_mixed"],
		"the role, the default model, the missing name, the repeat and its own model are dropped")

	// The editor shows the converted list and the new name, and saving that text as it is changes nothing.
	mixed, err := svc.Profiles().GetProfile(ctx, "mixed")
	require.NoError(t, err)
	assert.Equal(t, "name: mixed\ndescription: d\nruntime: codex\n"+
		"model: {provider: codex-subscription, name: gpt-6.2-sol}\ninstructions: Work carefully.\n"+
		"fallbacks: [codex-subscription/gpt-6.1-sol]\n", mixed.Source)

	same, created, err := svc.Profiles().ApplyProfile(ctx, "mixed", mixed.Source)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "pfv_mixed", same.VersionID)

	var instructions, botInstructions, botFallbacks string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT instructions FROM profile_versions WHERE id = 'pfv_im4'`).
		Scan(&instructions))
	assert.Equal(t, "Work carefully.", instructions)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT instructions, fallbacks FROM bots WHERE id = 'bot_maps'`).
		Scan(&botInstructions, &botFallbacks))
	assert.Equal(t, "You answer about maps.", botInstructions)
	assert.Equal(t, "claude-subscription/claude-sonnet-5-5,opencode-go/deepseek-v4.1-flash", botFallbacks,
		"a Bot's fallbacks are unchanged")

	_, err = p.DownTo(ctx, own-1)
	require.NoError(t, err)

	got = fallbacks()
	assert.Equal(t, "codex-worker", got["pfv_im3"])
	assert.Equal(t, "implementer-codex", got["pfv_im6"])
	assert.Equal(t, "explorer,plain,codex-worker,missing,codex-reviewer,implementer-codex", got["pfv_mixed"])
	require.NoError(t, db.QueryRowContext(ctx, `SELECT persona FROM bots WHERE id = 'bot_maps'`).
		Scan(&botInstructions))
	assert.Equal(t, "You answer about maps.", botInstructions)
}
