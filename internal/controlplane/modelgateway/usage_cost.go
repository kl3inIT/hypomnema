package modelgateway

import (
	"context"
	"database/sql"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/prices"
)

// price reckons a request's cost from the price book when it is recorded,
// as Coder snapshots a cost on its usage row (aibridgedserver/cost.go): the
// model asked for first, then the provider's, under the provider's vendor. A
// provider that cannot be read leaves the vendor to the model's name.
//
// What is not counted in tokens is priced by its unit (issue #723): a web
// search by its query at the provider that answered, a transcription by the
// seconds heard and speech by the characters spoken at the provider's model,
// and a model's own web searches by the search, added to its tokens'. A
// model whose tokens have no price stays unpriced, its searches too.
func (s *Service) price(ctx context.Context, u *Usage) {
	switch {
	case u.Model == ModelWebSearch:
		s.priceUnits(u, prices.UnitQuery, u.Provider+"/search", u.WebSearches*1000)

		return
	case strings.HasPrefix(u.Model, ModelAudioPrefix) && u.Audio > 0:
		s.priceUnits(u, prices.UnitSecond, u.Provider+"/"+u.UpstreamModel, u.Audio.Milliseconds())

		return
	case strings.HasPrefix(u.Model, ModelAudioPrefix):
		s.priceUnits(u, prices.UnitCharacter, u.Provider+"/"+u.UpstreamModel, u.Characters*1000)

		return
	}

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
		CacheWrite1h: u.Tokens.CacheWrite1h, Output: u.Tokens.Completion,
	})

	if u.WebSearches > 0 {
		if search, ok := prices.LookupWebSearch(kind, u.Model, u.UpstreamModel); ok {
			cost += prices.UnitCost(search, u.WebSearches*1000)
		}
	}

	u.CostMicros, u.PricedModel = &cost, p.Model
}

// priceUnits prices a request by its amount of a unit, in thousandths of it.
// Nothing of it is no cost to show; a price the book lacks leaves it
// unpriced.
func (s *Service) priceUnits(u *Usage, unit, name string, thousandths int64) {
	if thousandths <= 0 {
		return
	}

	p, ok := prices.LookupUnit(unit, name)
	if !ok {
		return
	}

	cost := prices.UnitCost(p, thousandths)
	u.CostMicros, u.PricedModel = &cost, p.Name
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
