package modelgateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/antigravity"
	store "github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

// Rewrite is one owner-declared find→replace rule for the Antigravity system instruction (issue #591): an empty
// With deletes the found text.
type Rewrite struct {
	Find string `json:"find"`
	With string `json:"with"`
}

// AntigravityRewrites are the owner's rules for the Antigravity system instruction, in order. The list is empty by
// default; Hypomnema ships none.
func (s *Service) AntigravityRewrites(ctx context.Context) ([]Rewrite, error) {
	rows, err := store.New(s.db).ListAntigravityRewrites(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the Antigravity rewrites: %w", err)
	}

	out := make([]Rewrite, 0, len(rows))
	for _, r := range rows {
		out = append(out, Rewrite{Find: r.Find, With: r.Replacement})
	}

	return out, nil
}

// SetAntigravityRewrites replaces every rule with these, in the order given. A rule whose Find is empty or blank is
// refused, as it would match nothing meaningful; the owner, who accepts the account risk, writes each one.
func (s *Service) SetAntigravityRewrites(ctx context.Context, rules []Rewrite) error {
	for i, r := range rules {
		if strings.TrimSpace(r.Find) == "" {
			return fmt.Errorf("%w: rule %d has no text to find", ErrInvalid, i+1)
		}
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.DeleteAntigravityRewrites(ctx); err != nil {
			return fmt.Errorf("clearing the Antigravity rewrites: %w", err)
		}

		for i, r := range rules {
			err := q.InsertAntigravityRewrite(ctx, store.InsertAntigravityRewriteParams{
				Position: int64(i), Find: r.Find, Replacement: r.With, Now: now,
			})
			if err != nil {
				return fmt.Errorf("writing Antigravity rewrite %d: %w", i+1, err)
			}
		}

		return nil
	})
}

// antigravityReplacements reads the owner's rules as the request builder takes them. A failed read is logged by the
// caller and treated as none: a flagged prompt is better than a dropped request.
func (s *Service) antigravityReplacements(ctx context.Context) []antigravity.Replacement {
	rules, err := s.AntigravityRewrites(ctx)
	if err != nil {
		return nil
	}

	out := make([]antigravity.Replacement, 0, len(rules))
	for _, r := range rules {
		out = append(out, antigravity.Replacement{Find: r.Find, With: r.With})
	}

	return out
}
