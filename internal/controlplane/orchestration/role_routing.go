package orchestration

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

//go:embed role_routing.yaml
var defaultRoleRouting string

var roles = []string{"implementer", "planner", "explorer", "reviewer"}
var taskKinds = []string{"broad", "narrow", "quick"}
var errRoleEntry = errors.New("role entry unavailable")

const roleRuntime = "@role"

type roleEntry struct {
	Runtime  string `json:"runtime" yaml:"runtime"`
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
}

type roleRouting map[string]map[string][]roleEntry

func validTaskKind(kind string) bool { return slices.Contains(taskKinds, kind) }

func routingSource(ctx context.Context, q *store.Queries) (string, error) {
	source, err := q.GetRoleRouting(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultRoleRouting, nil
	}
	return source, err
}

func (s *Service) RoleRouting(ctx context.Context) (string, error) {
	return routingSource(ctx, s.read())
}

func (s *Service) ApplyRoleRouting(ctx context.Context, source string) error {
	var routing roleRouting
	if err := decodeStrict(source, &routing); err != nil {
		return err
	}
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if err := validateRouting(ctx, q, routing); err != nil {
			return err
		}
		return q.SetRoleRouting(ctx, source)
	})
}

func validateRouting(ctx context.Context, q *store.Queries, routing roleRouting) error {
	if len(routing) != len(roles) {
		return fmt.Errorf("%w: routing must contain %s", ErrInvalid, strings.Join(roles, ", "))
	}
	for _, role := range roles {
		if len(routing[role]) != len(taskKinds) {
			return fmt.Errorf("%w: %s must have broad, narrow and quick chains", ErrInvalid, role)
		}
		for _, kind := range taskKinds {
			chain := routing[role][kind]
			if len(chain) == 0 || len(chain) > MaxFallbacks+1 {
				return fmt.Errorf("%w: %s/%s must have 1 to %d entries", ErrInvalid, role, kind, MaxFallbacks+1)
			}
			vendors := map[string]bool{}
			for index, entry := range chain {
				if entry.Provider == "" || entry.Model == "" || entry.Runtime == botRuntime ||
					slices.Contains(chain[:index], entry) {
					return fmt.Errorf("%w: %s/%s entry %d needs a unique worker runtime, provider and model",
						ErrInvalid, role, kind, index+1)
				}
				task := store.Task{Kind: kind, Profile: role}
				if _, err := resolveRoleEntry(ctx, q, store.ProfileVersion{}, task, chain, index); err != nil {
					return fmt.Errorf("%s/%s entry %d: %w", role, kind, index+1, err)
				}
				vendors[modelVendor(entry.Runtime, entry.Model)] = true
			}
			if role == "reviewer" && (vendors[""] || len(vendors) < 2) {
				return fmt.Errorf("%w: reviewer/%s needs at least two identified vendors", ErrInvalid, kind)
			}
		}
	}
	return nil
}

func modelVendor(runtime, model string) string {
	model = model[strings.LastIndex(model, "/")+1:]
	switch {
	case strings.HasPrefix(model, "gpt-"), model == "" && runtime == "codex":
		return "openai"
	case strings.HasPrefix(model, "claude-"), slices.Contains([]string{"opus", "sonnet", "haiku"}, model),
		model == "" && runtime == "claude":
		return "anthropic"
	case strings.HasPrefix(model, "deepseek-"):
		return "deepseek"
	case strings.HasPrefix(model, "glm-"):
		return "zai"
	default:
		return ""
	}
}

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
	vendor := modelVendor(conv.Runtime, last.Model.String)
	if vendor == "" {
		return "", fmt.Errorf("%w: cannot identify the vendor of author Task %s", ErrInvalid, author.ID)
	}
	return vendor, nil
}

func roleChain(ctx context.Context, q *store.Queries, role, kind string) ([]roleEntry, error) {
	source, err := routingSource(ctx, q)
	if err != nil {
		return nil, err
	}
	var routing roleRouting
	if err := decodeStrict(source, &routing); err != nil {
		return nil, err
	}
	return routing[role][kind], nil
}

func roleEffort(role, kind string) string {
	if role == "planner" || role == "reviewer" || kind == "broad" {
		return "high"
	}
	if kind == "quick" {
		return "low"
	}
	return "medium"
}

func chosenRoleChain(ctx context.Context, q *store.Queries, task store.Task) ([]roleEntry, error) {
	chain, err := roleChain(ctx, q, task.Profile, task.Kind)
	if err != nil {
		return nil, err
	}
	chain = slices.DeleteFunc(chain, func(entry roleEntry) bool {
		return task.Profile == "reviewer" && modelVendor(entry.Runtime, entry.Model) == task.AuthorVendor ||
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
		vendor := modelVendor(runtime, task.Model.String)
		if vendor == "" || vendor == task.AuthorVendor {
			return nil, fmt.Errorf("%w: the reviewer needs a model from another vendor", ErrInvalid)
		}
	}
	provider, model, err := taskModelRef(task)
	if err != nil {
		return nil, err
	}
	if _, err := checkModel(ctx, q, runtime, provider, model, task.Effort.String); err != nil {
		return nil, err
	}
	return []roleEntry{{Runtime: runtime, Provider: provider, Model: model}}, nil
}

func resolveRoleEntry(ctx context.Context, q *store.Queries, version store.ProfileVersion, task store.Task,
	chain []roleEntry, index int,
) (resolved, error) {
	entry := chain[index]
	effort := or(task.Effort.String, roleEffort(task.Profile, task.Kind))
	if _, err := checkModel(ctx, q, entry.Runtime, entry.Provider, entry.Model, effort); err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotFound) {
			return resolved{}, fmt.Errorf("%w: %s/%s: %w", errRoleEntry, entry.Provider, entry.Model, err)
		}
		return resolved{}, err
	}
	version.Runtime, version.Provider, version.Model, version.Effort = entry.Runtime,
		nullString(entry.Provider), nullString(entry.Model), nullString(effort)
	r, err := resolveVersion(ctx, q, store.Task{}, version)
	r.chain, r.chainIndex = chain, index
	return r, err
}

func dispatchRoleChain(last store.Dispatch) ([]roleEntry, error) {
	var chain []roleEntry
	if err := json.Unmarshal([]byte(last.RoleChain), &chain); err != nil {
		return nil, fmt.Errorf("decoding dispatch %s chain: %w", last.ID, err)
	}
	if !last.ChainEntry.Valid || last.ChainEntry.Int64 < 1 || last.ChainEntry.Int64 > int64(len(chain)) {
		return nil, fmt.Errorf("dispatch %s has no valid chain entry", last.ID)
	}
	return chain, nil
}
