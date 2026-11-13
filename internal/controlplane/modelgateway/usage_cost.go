package modelgateway

import (
	"context"
	"database/sql"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
)

// price reckons a request's cost from the price book when it is recorded,
// as Coder snapshots a cost on its usage row (aibridgedserver/cost.go): the
// model asked for first, then the provider's, under the provider's vendor. A
// provider that cannot be read leaves the vendor to the model's name.
func (s *Service) price(ctx context.Context, u *Usage) {
	if u.Tokens.Prompt == 0 && u.Tokens.Completion == 0 {
		return
	}

	kind := ""
	if p, err := s.providers(ctx, u.Provider); err == nil {
		kind = p.Kind
	}

	p, ok := prices.Lookup(kind, u.Model, u.UpstreamModel)
	if !ok {
		return
	}

	cost := prices.Cost(p, prices.Tokens{
		Input: u.Tokens.Prompt, CacheRead: u.Tokens.Cached, CacheWrite: u.Tokens.CacheWrite,
		Output: u.Tokens.Completion,
	})
	u.CostMicros, u.PricedModel = &cost, p.Model
}

func nullable(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: *v, Valid: true}
}

func fromNullable(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}

	return &v.Int64
}
