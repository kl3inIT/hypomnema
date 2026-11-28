package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
)

// The role routing is profilestore's (issue #1241); what stays here is how a
// Task takes it: the vendor a review must differ from, the chain a Task runs
// down and the entry a Dispatch runs.

func taskAuthor(ctx context.Context, q *store.Queries, run store.Run, nt NewTask) (string, error) {
	if nt.Profile != "reviewer" || nt.ReviewOf == "" {
		if nt.ReviewOf != "" {
			return "", fmt.Errorf("%w: review_of is only for a reviewer Task", ErrInvalid)
		}
		return "", nil
	}
	author, err := q.GetTask(ctx, nt.ReviewOf)
	if err != nil {
		return "", notFound(err, "task", nt.ReviewOf)
	}
	if author.RunID != run.ID || TaskStatus(author.Status) != TaskSucceeded {
		return "", fmt.Errorf("%w: review_of must name a succeeded Task of this Run", ErrInvalid)
	}
	last, err := q.LastDispatchOfTask(ctx, author.ID)
	if err != nil {
		return "", notFound(err, "author dispatch", author.ID)
	}
	conv, err := q.GetConversation(ctx, last.ConversationID)
	if err != nil {
		return "", notFound(err, "conversation", last.ConversationID)
	}
	vendor := profilestore.ModelVendor(conv.Runtime, last.Model.String)
	if vendor == "" {
		return "", fmt.Errorf("%w: cannot identify the vendor of author Task %s", ErrInvalid, author.ID)
	}
	return vendor, nil
}

func chosenRoleChain(ctx context.Context, q *store.Queries, task store.Task) ([]profilestore.RoleEntry, error) {
	chain, err := profilestore.RoleChainTx(ctx, q, task.Profile, task.Kind)
	if err != nil {
		return nil, err
	}
	chain = slices.DeleteFunc(chain, func(entry profilestore.RoleEntry) bool {
		return task.Profile == "reviewer" && profilestore.ModelVendor(entry.Runtime, entry.Model) == task.AuthorVendor ||
			task.Runtime.Valid && entry.Runtime != task.Runtime.String
	})
	if !task.Model.Valid {
		return chain, nil
	}
	runtime := task.Runtime.String
	if runtime == "" && len(chain) > 0 {
		runtime = chain[0].Runtime
	}
	if task.Profile == "reviewer" {
		vendor := profilestore.ModelVendor(runtime, task.Model.String)
		if vendor == "" || vendor == task.AuthorVendor {
			return nil, fmt.Errorf("%w: the reviewer needs a model from another vendor", ErrInvalid)
		}
	}
	provider, model, err := profilestore.ModelRef(task.Model.String)
	if err != nil {
		return nil, err
	}
	if _, err := profilestore.CheckModelTx(ctx, q, runtime, provider, model, task.Effort.String); err != nil {
		return nil, err
	}
	return []profilestore.RoleEntry{{Runtime: runtime, Provider: provider, Model: model}}, nil
}

func resolveRoleEntry(ctx context.Context, q *store.Queries, version store.ProfileVersion, task store.Task,
	chain []profilestore.RoleEntry, index int,
) (resolved, error) {
	entry := chain[index]
	// A role's effort is its routing's; a profile's own fallbacks run with the profile's effort (issue #1336).
	effort := or(task.Effort.String, profilestore.RoleEffort(task.Profile, task.Kind))
	if version.Runtime != profilestore.RoleRuntime {
		effort = or(task.Effort.String, version.Effort.String)
	}
	if err := profilestore.CheckRoleEntryTx(ctx, q, entry, effort); err != nil {
		return resolved{}, err
	}
	version.Runtime, version.Provider, version.Model, version.Effort = entry.Runtime,
		nullString(entry.Provider), nullString(entry.Model), nullString(effort)
	r, err := resolveVersion(ctx, q, store.Task{}, version)
	r.chain, r.chainIndex = chain, index
	return r, err
}

func dispatchRoleChain(last store.Dispatch) ([]profilestore.RoleEntry, error) {
	var chain []profilestore.RoleEntry
	if err := json.Unmarshal([]byte(last.RoleChain), &chain); err != nil {
		return nil, fmt.Errorf("decoding dispatch %s chain: %w", last.ID, err)
	}
	if !last.ChainEntry.Valid || last.ChainEntry.Int64 < 1 || last.ChainEntry.Int64 > int64(len(chain)) {
		return nil, fmt.Errorf("dispatch %s has no valid chain entry", last.ID)
	}
	return chain, nil
}
