package orchestration

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
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
// version's own) and every backup of a version with no model of its own, renames persona and instruction to instructions, and leaves the Bots' fallbacks as they are. Going
// down puts back the names.
func TestTheMigrationTurnsBackupProfilesIntoTheirModels(t *testing.T) {
	t.Parallel()

	own := migrationVersion(t, "_agent_spec.sql")
	svc, db := migratedTo(t, own-1)
	ctx := t.Context()

	l := legacyProfiles{t: t, db: db}
	exec, version, profile := l.exec, l.version, l.profile

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
	assert.Empty(t, got["pfv_im3"], "a version with no model of its own has no fallbacks")
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

// legacyProfiles writes profiles as they were before the migration: their versions' instruction under its old name
// and their fallbacks naming backup profiles.
type legacyProfiles struct {
	t  *testing.T
	db *sql.DB
}

func (l legacyProfiles) exec(query string, args ...any) {
	l.t.Helper()

	_, err := l.db.ExecContext(l.t.Context(), query, args...)
	require.NoError(l.t, err)
}

func (l legacyProfiles) version(id, profile string, n int, runtime, provider, model, effort, fallbacks string) {
	l.t.Helper()
	l.exec(`INSERT INTO profile_versions (id, profile, version, source, digest, runtime, provider, model, effort,
			instruction, fallbacks, description, created_at)
		VALUES (?, ?, ?, 'name: '||?, 'd', ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), 'Work carefully.', ?,
			'd', 100)`, id, profile, n, profile, runtime, provider, model, effort, fallbacks)
}

func (l legacyProfiles) profile(name, current string, deleted bool) {
	l.t.Helper()

	var at sql.NullInt64
	if deleted {
		at = sql.NullInt64{Int64: 200, Valid: true}
	}

	l.exec(`INSERT INTO profiles (name, current_version_id, created_at, updated_at, deleted_at)
		VALUES (?, ?, 1, 1, ?)
		ON CONFLICT (name) DO UPDATE SET current_version_id = excluded.current_version_id,
			deleted_at = excluded.deleted_at`, name, current, at)
}

// applied is a profile, with one version, whose YAML says what its row does, as the editor and the CLI wrote it
// before the migration.
func (l legacyProfiles) applied(name, runtime, provider, model, effort, fallbacks string) {
	l.t.Helper()

	source := "name: " + name + "\ndescription: d\nruntime: " + runtime + "\n"
	if model != "" {
		source += fmt.Sprintf("model: {provider: %s, name: %s", provider, model)
		if effort != "" {
			source += ", effort: " + effort
		}

		source += "}\n"
	}

	source += "instruction: Work carefully.\n"
	if fallbacks != "" {
		source += "fallbacks: [" + strings.ReplaceAll(fallbacks, ",", ", ") + "]\n"
	}

	id := "pfv_" + name
	l.profile(name, id, false)
	l.version(id, name, 1, runtime, provider, model, effort, fallbacks)
	l.exec(`UPDATE profile_versions SET source = ? WHERE id = ?`, source, id)
}

// Scenario (issue #1336, review of PR #1344): the migration gives a version only fallbacks the API accepts and its
// runtime can run. A backup whose model the version's runtime cannot run against (a Claude backup of a Codex profile),
// one whose model the catalog no longer offers, and every backup of a version with no model of its own are left out;
// a Codex backup of a Claude profile stays, since Claude Code runs it through the gateway. Each migrated profile, read
// and saved back as it is, is accepted and changes nothing, and the scheduler can run every entry of its chain. Going
// down puts back each list as it was, and the YAML of a version saved since in the names of this release is written
// back in the names the release before reads: instruction, and no fallbacks, which named models.
func TestTheMigrationKeepsOnlyFallbacksTheRuntimeRuns(t *testing.T) {
	t.Parallel()

	own := migrationVersion(t, "_agent_spec.sql")
	svc, db := migratedTo(t, own-1)
	ctx := t.Context()
	l := legacyProfiles{t: t, db: db}

	l.exec(`INSERT OR IGNORE INTO models (provider, name) VALUES ('claude-subscription', 'claude-opus-5-5'),
		('claude-subscription', 'claude-sonnet-5-5'), ('codex-subscription', 'gpt-6.1-sol'),
		('codex-subscription', 'gpt-6.2-sol')`)
	l.applied("claude-backup", "claude", "claude-subscription", "claude-sonnet-5-5", "", "")
	l.applied("codex-backup", "codex", "codex-subscription", "gpt-6.1-sol", "", "")
	l.applied("old-backup", "codex", "codex-subscription", "gpt-5-gone", "", "")
	l.applied("codex-default", "codex", "", "", "", "claude-backup,codex-backup")
	l.applied("codex-model", "codex", "codex-subscription", "gpt-6.2-sol", "",
		"claude-backup,codex-backup,old-backup")
	l.applied("claude-model", "claude", "claude-subscription", "claude-opus-5-5", "high",
		"codex-backup,claude-backup")

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, own)
	require.NoError(t, err)

	q := svc.read()
	want := map[string]string{
		"codex-default": "",
		"codex-model":   "codex-subscription/gpt-6.1-sol",
		"claude-model":  "codex-subscription/gpt-6.1-sol,claude-subscription/claude-sonnet-5-5",
	}

	for name, fallbacks := range want {
		v, err := q.CurrentProfileVersion(ctx, name)
		require.NoError(t, err)
		assert.Equal(t, fallbacks, v.Fallbacks, name)

		read, err := svc.Profiles().GetProfile(ctx, name)
		require.NoError(t, err)

		same, created, err := svc.Profiles().ApplyProfile(ctx, name, read.Source)
		require.NoError(t, err, "%s saved back as read:\n%s", name, read.Source)
		assert.False(t, created)
		assert.Equal(t, v.ID, same.VersionID, "%s saved back changes nothing", name)

		chain := profileChain(v, store.Task{})
		if fallbacks == "" {
			assert.Empty(t, chain, name)

			continue
		}

		require.Len(t, chain, len(splitNames(fallbacks))+1, name)

		for _, entry := range chain {
			require.NoError(t, profilestore.CheckRoleEntryTx(ctx, q, entry, v.Effort.String), "%s runs %s/%s", name,
				entry.Provider, entry.Model)
		}
	}

	_, _, err = svc.Profiles().ApplyProfile(ctx, "codex-model", "name: codex-model\ndescription: d\nruntime: codex\n"+
		"model: {provider: codex-subscription, name: gpt-6.2-sol}\ninstructions: Be brief.\n"+
		"fallbacks: [codex-subscription/gpt-6.1-sol]\n")
	require.NoError(t, err)
	_, _, err = svc.Profiles().ApplyProfile(ctx, "claude-model", "name: claude-model\ndescription: d\n"+
		"runtime: claude\nmodel: {provider: claude-subscription, name: claude-opus-5-5}\n"+
		"fallbacks:\n  - claude-subscription/claude-sonnet-5-5\ninstructions: |\n  Line one.\n"+
		"  instructions: kept as written\n")
	require.NoError(t, err)

	_, err = p.DownTo(ctx, own-1)
	require.NoError(t, err)

	stored := func(query, id string) string {
		t.Helper()

		var out string
		require.NoError(t, db.QueryRowContext(ctx, query, id).Scan(&out))

		return out
	}
	current := `SELECT v.source FROM profile_versions AS v JOIN profiles AS p ON p.current_version_id = v.id
		WHERE p.name = ?`
	fallbacks := `SELECT fallbacks FROM profile_versions WHERE id = ?`

	assert.Equal(t, "claude-backup,codex-backup", stored(fallbacks, "pfv_codex-default"))
	assert.Equal(t, "claude-backup,codex-backup,old-backup", stored(fallbacks, "pfv_codex-model"))
	assert.Equal(t, "codex-backup,claude-backup", stored(fallbacks, "pfv_claude-model"))
	assert.Equal(t, "name: codex-model\ndescription: d\nruntime: codex\n"+
		"model: {provider: codex-subscription, name: gpt-6.2-sol}\ninstruction: Work carefully.\n"+
		"fallbacks: [claude-backup, codex-backup, old-backup]\n",
		stored(`SELECT source FROM profile_versions WHERE id = ?`, "pfv_codex-model"), "a version from before is as written")
	assert.Equal(t, "name: codex-model\ndescription: d\nruntime: codex\n"+
		"model: {provider: codex-subscription, name: gpt-6.2-sol}\ninstruction: Be brief.\n",
		stored(current, "codex-model"))
	assert.Equal(t, "name: claude-model\ndescription: d\nruntime: claude\n"+
		"model: {provider: claude-subscription, name: claude-opus-5-5}\ninstruction: |\n  Line one.\n"+
		"  instructions: kept as written\n", stored(current, "claude-model"))
}
