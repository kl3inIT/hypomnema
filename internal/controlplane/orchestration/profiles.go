package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
)

// Profiles, their versions, the role routing and the providers and models they
// name are profilestore's, reached through Profiles() (issue #1241). What stays
// here is what spans the scheduler and the setup changes: a profile the main
// Bot changes from its chat with what undoes it, whether a worker can start
// under a profile now, and what a Task's Dispatch runs with.

// Profiles returns the store of the profiles, their versions, the role
// routing and the providers and models they name, over the same connection,
// clock and ids, waking the same event readers. A profile changed from the
// main Bot's chat, which records a setup change too, stays here
// (ApplyProfileFromChat, RestoreProfileVersion).
func (s *Service) Profiles() *profilestore.Store { return s.profiles }

// RestoreProfileVersion applies one of a profile's versions again, as the profile page restores it: its own source,
// with its tool policy as stored. With a chat, the new version is the Bot coordinator's from that chat, as
// applyProfile's.
func (s *Service) RestoreProfileVersion(ctx context.Context, chat, name, versionID string,
) (profilestore.Profile, bool, error) {
	row, err := s.read().GetProfileVersion(ctx, versionID)
	if err != nil || row.Profile != name {
		return profilestore.Profile{}, false, fmt.Errorf("%w: version %s of profile %s", ErrNotFound, versionID,
			name)
	}

	// The version as the page shows it: its tool policy as migration 00123 re-expressed it, never the old allowlist
	// its YAML still writes, which would now widen it (EditableSource; review of PR #1000).
	source := profilestore.EditableSource(row)

	return s.applyProfile(ctx, chat, name, source)
}

// applyProfile is profilestore's ApplyProfile; with a chat, a new version is
// the Bot coordinator's from that chat and is recorded with what undoes it
// (setupchanges.go).
func (s *Service) applyProfile(ctx context.Context, chat, name, source string) (profilestore.Profile, bool, error) {
	d, err := profilestore.ParseProfile(name, source)
	if err != nil {
		return profilestore.Profile{}, false, err
	}

	var (
		out     profilestore.Profile
		created bool
	)

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := changeFromChat(ctx, q, chat); err != nil {
			return err
		}

		var (
			before string
			err    error
		)

		out, created, before, err = s.profiles.ApplyTx(ctx, q, now, d)

		switch {
		case err != nil, !created, chat == "":
			return err
		case before == "":
			return s.recordChange(ctx, q, now, chat, changeProfile, out.Name, actionMade, nil, out.VersionID)
		default:
			return s.recordChange(ctx, q, now, chat, changeProfile, out.Name, actionChanged, before, out.VersionID)
		}
	})
	if err != nil {
		return profilestore.Profile{}, false, fmt.Errorf("applying a profile: %w", err)
	}

	return out, created, nil
}

// profilePlacement says of each profile whether a worker can start under it
// now, from the scheduler's own view of the nodes and accounts (Omnigent's
// agent list carries available_on_host and unavailable_reason the same way).
type profilePlacement struct{ s *Service }

func (p profilePlacement) SetAvailability(ctx context.Context, profiles []profilestore.Profile) error {
	q := p.s.read()

	pl, err := loadPlacement(ctx, q, p.s.clock.Now().UnixMilli())
	if err != nil {
		return err
	}

	for i, pr := range profiles {
		if pr.Runtime == "" {
			continue
		}
		gateway, accounts := false, ""

		if pr.Provider != "" {
			row, err := q.GetProvider(ctx, pr.Provider)
			if err != nil {
				return notFound(err, "provider", pr.Provider)
			}

			gateway, accounts = profilestore.ThroughGateway(row.Kind), subscriptionAccounts[row.Kind]
		}

		a := pl.availability(pr.Runtime, gateway, accounts)
		profiles[i].Availability = &a
	}

	return nil
}

// subscriptionAccounts is the runtime of the accounts logged in on the control plane that each subscription runs
// under, as the gateway's adapters ask for them (modelgateway codexRuntime, antigravityRuntime; TokenRuntime).
var subscriptionAccounts = map[string]string{
	profilestore.ProviderClaudeSubscription:      TokenRuntime,
	profilestore.ProviderCodexSubscription:       "codex",
	profilestore.ProviderAntigravitySubscription: "antigravity",
	profilestore.ProviderOpenCodeGo:              OpenCodeGoRuntime,
	profilestore.ProviderOpenCodeZen:             OpenCodeZenRuntime,
}

// PiModel is a chat's model as pi names it: the gateway's name under PiProvider when the gateway serves it, as it
// was otherwise. A model without its provider is refused with the form to write it in, never passed on bare.
func (s *Service) PiModel(ctx context.Context, m ChatModel) (ChatModel, error) {
	if m.Model == "" {
		return m, nil
	}

	if m.Provider == "" {
		return ChatModel{}, profilestore.BareModel(botRuntime, m.Model, nil)
	}

	row, err := s.read().GetProvider(ctx, m.Provider)
	if err != nil {
		return ChatModel{}, notFound(err, "provider", m.Provider)
	}

	if profilestore.ThroughGateway(row.Kind) {
		m.Model, m.Provider = profilestore.GatewayModelName(row.Kind, m.Provider, m.Model), profilestore.PiProvider
	}

	return m, nil
}

// checkTaskProfile checks a new Task's profile and its overrides together.
func checkTaskProfile(ctx context.Context, q *store.Queries, nt NewTask, vendor string) error {
	v, err := q.CurrentProfileVersion(ctx, nt.Profile)
	if err != nil {
		return fmt.Errorf("task %q: %w", nt.Ref, profilestore.MissingProfileTx(ctx, q, err, nt.Profile))
	}
	if v.Runtime == profilestore.RoleRuntime || vendor != "" {
		if nt.Profile == "reviewer" && nt.ReviewOf == "" {
			return fmt.Errorf("%w: reviewer Task %q needs review_of", ErrInvalid, nt.Ref)
		}
		if nt.Effort != "" && !slices.Contains(profilestore.Efforts, nt.Effort) {
			return fmt.Errorf("%w: unknown effort %q", ErrInvalid, nt.Effort)
		}
		if nt.Runtime != "" || nt.Model != "" {
			task := store.Task{Profile: nt.Profile, Kind: nt.Kind, AuthorVendor: vendor,
				Runtime: nullString(nt.Runtime), Model: nullString(nt.Model), Effort: nullString(nt.Effort)}
			chain, err := chosenRoleChain(ctx, q, task)
			if err != nil {
				return err
			}
			if len(chain) == 0 {
				return fmt.Errorf("%w: requested runtime %s is not in the Task's role chain", ErrInvalid, nt.Runtime)
			}
		}
		return nil
	}

	if nt.Runtime == "" && nt.Model == "" && nt.Effort == "" {
		return nil
	}

	runtime := or(nt.Runtime, v.Runtime)

	provider, model := v.Provider.String, nt.Model
	if nt.Model != "" || nt.Runtime != "" {
		var err error
		// A Task's model names its own provider (#1002).
		if provider, model, err = profilestore.ModelRef(nt.Model); err != nil {
			return fmt.Errorf("task %q: %w", nt.Ref, err)
		}
	}

	if _, err := profilestore.CheckModelTx(ctx, q, runtime, provider, model, nt.Effort); err != nil {
		return fmt.Errorf("task %q: %w", nt.Ref, err)
	}

	return nil
}

// resolved is what a Dispatch runs with: the Task's own values over its
// profile's current version.
type resolved struct {
	runtime, model, effort string
	// profile and versionID are the profile version the values come from.
	profile, versionID string
	// gateway says the model gateway serves the model, so no account on a
	// node is needed.
	gateway bool
	// accounts is the runtime of the control plane's accounts the gateway serves the model under, for a
	// subscription's: without an active one, nothing can serve it.
	accounts string
	// fallbackFor is the Task's own profile when these are a backup's values,
	// and fallbackUntil when that profile's accounts are free again.
	fallbackFor   string
	fallbackUntil int64
	// handoff says the backup takes over work another worker began.
	handoff bool
	// req is what a worker of the profile version asks of a node (issue #731).
	req          request
	chain        []profilestore.RoleEntry
	chainIndex   int
	gatewayUntil int64
	gatewayRoute string
}

func resolve(ctx context.Context, q *store.Queries, task store.Task) (resolved, error) {
	v, err := q.CurrentProfileVersion(ctx, task.Profile)
	if err != nil {
		return resolved{}, notFound(err, "profile", task.Profile)
	}

	// A Task that runs under a backup of its profile keeps that backup for
	// the rest of its open Conversation: a later Dispatch continues a session
	// of the backup's CLI, with the backup's model and instruction.
	last, err := q.LastDispatchOfTask(ctx, task.ID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return resolved{}, fmt.Errorf("reading the last dispatch of task %s: %w", task.ID, err)
	case last.RoleChain != "":
		chain, err := dispatchRoleChain(last)
		if err != nil {
			return resolved{}, err
		}
		version, err := q.GetProfileVersion(ctx, last.ProfileVersionID.String)
		if err != nil {
			return resolved{}, notFound(err, "profile version", last.ProfileVersionID.String)
		}
		r, err := resolveRoleEntry(ctx, q, version, task, chain, int(last.ChainEntry.Int64)-1)
		r.fallbackFor, r.fallbackUntil = last.FallbackFor.String, last.FallbackUntil.Int64
		if FailureClass(last.FailureClass.String) == FailureRateLimited {
			r.gatewayUntil = last.ResetsAt.Int64
		}
		return r, err
	case v.Runtime == profilestore.RoleRuntime && last.ProfileVersionID.Valid:
		version, err := q.GetProfileVersion(ctx, last.ProfileVersionID.String)
		if err != nil {
			return resolved{}, notFound(err, "profile version", last.ProfileVersionID.String)
		}
		return resolveVersion(ctx, q, task, version)
	case last.FallbackFor.Valid && last.ProfileVersionID.Valid:
		conv, open, err := openConversation(ctx, q, task.ID)
		if err != nil {
			return resolved{}, err
		}

		if open && conv.ID == last.ConversationID {
			b, err := q.GetProfileVersion(ctx, last.ProfileVersionID.String)
			if err != nil {
				return resolved{}, notFound(err, "profile version", last.ProfileVersionID.String)
			}

			r, err := resolveVersion(ctx, q, task, b)
			r.fallbackFor, r.fallbackUntil = last.FallbackFor.String, last.FallbackUntil.Int64

			return r, err
		}
	}
	if v.Runtime == profilestore.RoleRuntime || task.AuthorVendor != "" {
		chain, err := chosenRoleChain(ctx, q, task)
		if err != nil {
			return resolved{}, err
		}
		if len(chain) == 0 {
			return resolved{}, fmt.Errorf("%w: no entry for %s/%s matches the Task", profilestore.ErrRoleEntry,
				task.Profile, task.Kind)
		}
		return resolveRoleEntry(ctx, q, v, task, chain, 0)
	}

	return resolveVersion(ctx, q, task, v)
}

// resolveVersion is the Task's own values over one profile version.
func resolveVersion(ctx context.Context, q *store.Queries, task store.Task, v store.ProfileVersion,
) (resolved, error) {
	model, provider := v.Model.String, v.Provider.String
	if task.Runtime.String != "" && task.Runtime.String != v.Runtime {
		model, provider = "", "" // the profile's model and provider belong to its runtime
	}

	runtime := or(task.Runtime.String, v.Runtime)

	// A Task's own model names its own provider, "<provider>/<model>" (issue #1002), as checkTaskProfile checked it.
	if task.Model.String != "" {
		var err error
		if provider, model, err = profilestore.ModelRef(task.Model.String); err != nil {
			return resolved{}, err
		}

		if _, err := profilestore.CheckModelTx(ctx, q, runtime, provider, model, ""); err != nil {
			return resolved{}, err
		}
	}

	gateway, accounts := false, ""

	if provider != "" {
		row, err := q.GetProvider(ctx, provider)
		if err != nil {
			return resolved{}, notFound(err, "provider", provider)
		}

		gateway, accounts = profilestore.ThroughGateway(row.Kind), subscriptionAccounts[row.Kind]
		if model != "" {
			model = profilestore.GatewayModelName(row.Kind, provider, model)
		}
	}
	var route string
	if gateway {
		var err error
		route, err = gatewayRoute(ctx, q, runtime, model)
		if err != nil {
			return resolved{}, err
		}
	}

	return resolved{
		runtime: runtime, model: model,
		effort: or(task.Effort.String, v.Effort.String), profile: v.Profile, versionID: v.ID, gateway: gateway,
		accounts: accounts, req: requestOf(v), gatewayRoute: route,
	}, nil
}
