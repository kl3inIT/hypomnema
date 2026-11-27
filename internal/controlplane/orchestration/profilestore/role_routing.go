package profilestore

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// DefaultRoleRouting is the role routing until the owner applies one.
//
//go:embed role_routing.yaml
var DefaultRoleRouting string

// Roles are the profiles a Task's kind routes, by role routing, to a chain of runtimes and models.
var Roles = []string{"implementer", "planner", "explorer", "reviewer"}

var taskKinds = []string{"broad", "narrow", "quick"}

// ErrRoleEntry is an entry of a role's chain that cannot run now: its model is not served.
var ErrRoleEntry = errors.New("role entry unavailable")

// RoleRuntime stands for the runtime of a role's version: its chain names the runtimes.
const RoleRuntime = "@role"

// RoleEntry is one runtime, provider and model of a role's chain.
type RoleEntry struct {
	Runtime  string `json:"runtime" yaml:"runtime"`
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
}

type roleRouting map[string]map[string][]RoleEntry

// ValidTaskKind reports whether a Task's kind is one role routing knows.
func ValidTaskKind(kind string) bool { return slices.Contains(taskKinds, kind) }

func routingSource(ctx context.Context, q *store.Queries) (string, error) {
	source, err := q.GetRoleRouting(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultRoleRouting, nil
	}
	return source, err
}

// RoleRouting returns the role routing's YAML.
func (s *Store) RoleRouting(ctx context.Context) (string, error) {
	return routingSource(ctx, s.read())
}

// ApplyRoleRouting stores the role routing's YAML once each entry of each chain is served.
func (s *Store) ApplyRoleRouting(ctx context.Context, source string) error {
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
	if len(routing) != len(Roles) {
		return fmt.Errorf("%w: routing must contain %s", storeerr.ErrInvalid, strings.Join(Roles, ", "))
	}
	for _, role := range Roles {
		if len(routing[role]) != len(taskKinds) {
			return fmt.Errorf("%w: %s must have broad, narrow and quick chains", storeerr.ErrInvalid, role)
		}
		for _, kind := range taskKinds {
			chain := routing[role][kind]
			if len(chain) == 0 || len(chain) > MaxFallbacks+1 {
				return fmt.Errorf("%w: %s/%s must have 1 to %d entries", storeerr.ErrInvalid, role, kind, MaxFallbacks+1)
			}
			vendors := map[string]bool{}
			for index, entry := range chain {
				if entry.Provider == "" || entry.Model == "" || entry.Runtime == BotRuntime ||
					slices.Contains(chain[:index], entry) {
					return fmt.Errorf("%w: %s/%s entry %d needs a unique worker runtime, provider and model",
						storeerr.ErrInvalid, role, kind, index+1)
				}
				if err := CheckRoleEntryTx(ctx, q, entry, RoleEffort(role, kind)); err != nil {
					return fmt.Errorf("%s/%s entry %d: %w", role, kind, index+1, err)
				}
				vendors[ModelVendor(entry.Runtime, entry.Model)] = true
			}
			if role == "reviewer" && (vendors[""] || len(vendors) < 2) {
				return fmt.Errorf("%w: reviewer/%s needs at least two identified vendors", storeerr.ErrInvalid, kind)
			}
		}
	}
	return nil
}

// ModelVendor is the vendor of a model, by its name, or by its runtime for none; "" when it cannot be told.
func ModelVendor(runtime, model string) string {
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

// RoleChainTx is the chain the role routing names for a role and a Task's kind.
func RoleChainTx(ctx context.Context, q *store.Queries, role, kind string) ([]RoleEntry, error) {
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

// RoleEffort is the effort a role works a Task of the kind with, unless the Task names one.
func RoleEffort(role, kind string) string {
	if role == "planner" || role == "reviewer" || kind == "broad" {
		return "high"
	}
	if kind == "quick" {
		return "low"
	}
	return "medium"
}

// CheckRoleEntryTx checks that an entry of a role's chain is served with the effort: ErrRoleEntry when its model is
// not.
func CheckRoleEntryTx(ctx context.Context, q *store.Queries, entry RoleEntry, effort string) error {
	if _, err := CheckModelTx(ctx, q, entry.Runtime, entry.Provider, entry.Model, effort); err != nil {
		if errors.Is(err, storeerr.ErrInvalid) || errors.Is(err, storeerr.ErrNotFound) {
			return fmt.Errorf("%w: %s/%s: %w", ErrRoleEntry, entry.Provider, entry.Model, err)
		}
		return err
	}
	return nil
}
