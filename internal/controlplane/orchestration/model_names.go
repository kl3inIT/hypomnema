package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Stored Task models named with their provider (issue #1002). Before it, a Task's own model was a bare name, and
// each Dispatch picked the one provider offering it to the Task's runtime (checkModel then). The migration
// *_models_named_with_provider.sql asks for the change; Migrate makes it right after that migration and before any
// later one (OpenCode Zen's seed, #965), with the code below: each Task's model gets the provider the code before
// would have dispatched it under, read the same way, and a name it would not have placed is left as it is and marked
// (tasks.model_unresolved): it is read whole, never at a slash it may have (taskModelRef), and refused with the form
// to write it in when the Task is dispatched, as the code before refused it; the scheduler holds that Task alone.
// Each change is kept in model_name_changes, which the way back restores.
//
// A profile version, a Bot or a chat keeps its provider beside its model. One stored with a model and no provider
// gets the one provider that offers the model to its runtime (the profile version's, pi's for a Bot and a chat),
// among the kinds before OpenCode Zen, as checkModel picked one when such a row was applied (review of PR #1000,
// round 6); each is kept in model_provider_changes for the way back. One that two providers or none offer is left
// as it is, and is refused with the form to write it in wherever it is used (resolveVersion, keptModel, PiModel).

// modelNamesMigration ends the file name of the migration that asks for the change, whatever its version.
const modelNamesMigration = "_models_named_with_provider.sql"

// nameStoredModels gives each stored bare model its provider, once.
func nameStoredModels(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("naming stored models: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a committed transaction rolls back nothing

	var done int
	switch err := tx.QueryRowContext(ctx, `SELECT done FROM model_name_conversion`).Scan(&done); {
	case err != nil && strings.Contains(err.Error(), "no such table"):
		return nil // the database is below the migration, or was taken back past it
	case err != nil:
		return fmt.Errorf("reading whether stored models are named: %w", err)
	case done != 0:
		return nil
	}

	routing, err := legacyRouting(ctx, tx)
	if err != nil {
		return err
	}

	tasks, err := legacyTasks(ctx, tx)
	if err != nil {
		return err
	}

	for _, t := range tasks {
		provider, ok, err := legacyTaskProvider(ctx, tx, routing, t)
		if err != nil {
			return err
		}

		if !ok {
			// Left as it was, read whole and refused when dispatched, as the code before refused it.
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET model_unresolved = 1 WHERE id = ?`, t.id); err != nil {
				return fmt.Errorf("marking the model of task %s: %w", t.id, err)
			}

			continue
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO model_name_changes (task_id, model) VALUES (?, ?)`,
			t.id, t.model); err != nil {
			return fmt.Errorf("recording the model of task %s: %w", t.id, err)
		}

		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET model = ? WHERE id = ?`,
			provider+"/"+t.model, t.id); err != nil {
			return fmt.Errorf("naming the model of task %s: %w", t.id, err)
		}
	}

	if err := nameProviderlessRows(ctx, tx); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE model_name_conversion SET done = 1`); err != nil {
		return fmt.Errorf("recording that stored models are named: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("naming stored models: %w", err)
	}

	return nil
}

// legacyTask is what of a Task its model was resolved from.
type legacyTask struct {
	id, profile, kind, runtime, model, authorVendor string
}

func legacyTasks(ctx context.Context, tx *sql.Tx) ([]legacyTask, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, profile, kind, coalesce(runtime, ''), model, author_vendor FROM tasks
		WHERE model IS NOT NULL AND model <> '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reading the models of tasks: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read only

	var out []legacyTask

	for rows.Next() {
		var t legacyTask
		if err := rows.Scan(&t.id, &t.profile, &t.kind, &t.runtime, &t.model, &t.authorVendor); err != nil {
			return nil, fmt.Errorf("reading the models of tasks: %w", err)
		}

		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the models of tasks: %w", err)
	}

	return out, nil
}

// legacyRouting is the role routing the code before read (routingSource).
func legacyRouting(ctx context.Context, tx *sql.Tx) (roleRouting, error) {
	source := defaultRoleRouting

	err := tx.QueryRowContext(ctx, `SELECT source FROM role_routing LIMIT 1`).Scan(&source)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading role routing: %w", err)
	}

	var routing roleRouting
	if err := decodeStrict(source, &routing); err != nil {
		return nil, err
	}

	return routing, nil
}

// legacyTaskProvider is the provider the code before dispatched a Task's model under, following resolve's branches:
// the role-chain entry its last Dispatch recorded; else the runtime of the version its last Dispatch ran (a role
// profile's, or a backup's in its open Conversation), of its role chain's first entry, or of its profile, each
// unless the Task names its own; and the one provider that offers the name to that runtime (checkModel). ok is
// false when it would not have placed the name.
func legacyTaskProvider(ctx context.Context, tx *sql.Tx, routing roleRouting, t legacyTask) (string, bool, error) {
	var (
		chain, versionID, fallbackFor, conversationID sql.NullString
		chainEntry                                    sql.NullInt64
	)

	err := tx.QueryRowContext(ctx, `
		SELECT role_chain, chain_entry, profile_version_id, fallback_for, conversation_id FROM dispatches
		WHERE task_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, t.id).
		Scan(&chain, &chainEntry, &versionID, &fallbackFor, &conversationID)
	hasLast := err == nil

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("reading the last dispatch of task %s: %w", t.id, err)
	}

	if hasLast && chain.String != "" {
		var entries []roleEntry
		if json.Unmarshal([]byte(chain.String), &entries) == nil && chainEntry.Valid &&
			chainEntry.Int64 >= 1 && chainEntry.Int64 <= int64(len(entries)) {
			entry := entries[chainEntry.Int64-1]

			return entry.Provider, entry.Model == t.model && entry.Provider != "", nil
		}

		return "", false, nil
	}

	current, ok, err := legacyVersionRuntime(ctx, tx,
		`SELECT v.runtime FROM profiles p JOIN profile_versions v ON v.id = p.current_version_id
		 WHERE p.name = ? AND p.deleted_at IS NULL`, t.profile)
	if err != nil || !ok {
		return "", false, err
	}

	var runtime string

	switch {
	case hasLast && versionID.Valid && current == roleRuntime:
		runtime, ok, err = legacyVersionRuntime(ctx, tx, `SELECT runtime FROM profile_versions WHERE id = ?`,
			versionID.String)
	case hasLast && versionID.Valid && fallbackFor.Valid && legacyOpenConversation(ctx, tx, t.id, conversationID):
		runtime, ok, err = legacyVersionRuntime(ctx, tx, `SELECT runtime FROM profile_versions WHERE id = ?`,
			versionID.String)
	case current == roleRuntime || t.authorVendor != "":
		runtime, ok = legacyChainRuntime(routing, t), true
	default:
		runtime, ok = current, true
	}

	if err != nil || !ok {
		return "", false, err
	}

	return legacyProvider(ctx, tx, or(t.runtime, runtime), t.model)
}

func legacyVersionRuntime(ctx context.Context, tx *sql.Tx, query, arg string) (string, bool, error) {
	var runtime string

	err := tx.QueryRowContext(ctx, query, arg).Scan(&runtime)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("reading a profile's runtime: %w", err)
	}

	return runtime, true, nil
}

// legacyOpenConversation says the Task's latest Conversation is open and is the one named (openConversation).
func legacyOpenConversation(ctx context.Context, tx *sql.Tx, taskID string, conversationID sql.NullString) bool {
	var id, status string

	err := tx.QueryRowContext(ctx, `
		SELECT id, status FROM conversations WHERE task_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, taskID).
		Scan(&id, &status)

	return err == nil && ConversationStatus(status) == ConversationOpen && id == conversationID.String
}

// legacyChainRuntime is the runtime chosenRoleChain gave a Task naming its own model: its own, else its role
// chain's first entry once the entries the Task cannot take are left out.
func legacyChainRuntime(routing roleRouting, t legacyTask) string {
	if t.runtime != "" {
		return t.runtime
	}

	chain := slices.DeleteFunc(slices.Clone(routing[t.profile][t.kind]), func(entry roleEntry) bool {
		return t.profile == "reviewer" && modelVendor(entry.Runtime, entry.Model) == t.authorVendor
	})
	if len(chain) == 0 {
		return ""
	}

	return chain[0].Runtime
}

// legacyProvider is the one provider offering a bare name to a runtime, as checkModel picked it before issue #1002:
// among the kinds the runtime runs against then, before OpenCode Zen. ok is false for none or several.
func legacyProvider(ctx context.Context, tx *sql.Tx, runtime, model string) (string, bool, error) {
	kinds, known := runtimeProviders[runtime]
	if !known {
		return "", false, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT m.provider, p.kind FROM models m JOIN providers p ON p.name = m.provider
		WHERE m.name = ? ORDER BY m.provider`, model)
	if err != nil {
		return "", false, fmt.Errorf("reading the providers of model %s: %w", model, err)
	}
	defer rows.Close() //nolint:errcheck // read only

	var fits []string

	for rows.Next() {
		var provider, kind string
		if err := rows.Scan(&provider, &kind); err != nil {
			return "", false, fmt.Errorf("reading the providers of model %s: %w", model, err)
		}

		if kind != ProviderOpenCodeZen && slices.Contains(kinds, kind) {
			fits = append(fits, provider)
		}
	}

	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("reading the providers of model %s: %w", model, err)
	}

	if len(fits) != 1 {
		return "", false, nil
	}

	return fits[0], true, nil
}

// providerless are the stored rows that name a model beside their provider, with the runtime each is run under.
var providerless = []struct{ record, table, runtime string }{
	{"profile_version", "profile_versions", "runtime"},
	{"bot", "bots", "'" + botRuntime + "'"},
	{"chat", "chats", "'" + botRuntime + "'"},
}

// providerlessRow is a stored row naming a model without a provider, with its runtime.
type providerlessRow struct{ id, model, runtime string }

// providerlessRows are a table's rows naming a model without a provider.
func providerlessRows(ctx context.Context, tx *sql.Tx, table, runtime string) ([]providerlessRow, error) {
	//nolint:gosec // the table and the runtime's expression are the constants of providerless
	q, err := tx.QueryContext(ctx, `SELECT id, model, `+runtime+` FROM `+table+`
		WHERE model IS NOT NULL AND model <> '' AND (provider IS NULL OR provider = '') ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reading the models of %s: %w", table, err)
	}
	defer q.Close() //nolint:errcheck // read only

	var rows []providerlessRow

	for q.Next() {
		var r providerlessRow
		if err := q.Scan(&r.id, &r.model, &r.runtime); err != nil {
			return nil, fmt.Errorf("reading the models of %s: %w", table, err)
		}

		rows = append(rows, r)
	}

	if err := q.Err(); err != nil {
		return nil, fmt.Errorf("reading the models of %s: %w", table, err)
	}

	return rows, nil
}

// nameProviderlessRows gives each profile version, Bot and chat that names a model without a provider the one
// provider that offers the model to its runtime, and records it.
func nameProviderlessRows(ctx context.Context, tx *sql.Tx) error {
	for _, kind := range providerless {
		rows, err := providerlessRows(ctx, tx, kind.table, kind.runtime)
		if err != nil {
			return err
		}

		for _, r := range rows {
			provider, ok, err := legacyProvider(ctx, tx, r.runtime, r.model)
			if err != nil {
				return err
			}

			if !ok {
				continue // two providers offer it, or none: refused where it is used
			}

			if _, err := tx.ExecContext(ctx, `INSERT INTO model_provider_changes (record, id, model, provider)
				VALUES (?, ?, ?, ?)`, kind.record, r.id, r.model, provider); err != nil {
				return fmt.Errorf("recording the provider of %s %s: %w", kind.record, r.id, err)
			}

			//nolint:gosec // the table is one of the constants above
			if _, err := tx.ExecContext(ctx, `UPDATE `+kind.table+` SET provider = ? WHERE id = ?`,
				provider, r.id); err != nil {
				return fmt.Errorf("naming the provider of %s %s: %w", kind.record, r.id, err)
			}
		}
	}

	return nil
}
