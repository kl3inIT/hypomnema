package modelgateway

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// What the owner sets on Settings → Gateway → Web search (issue #354): a
// switch for each search provider, and SearXNG's address. A provider that is
// off is neither tried nor may be named; each is on until the owner turns it
// off (owner, 2026-11-13). They are kept here because web search is the
// gateway's: it serves /v1/search, and the gateway_* tables are its own.

// SearchSettings reads what the owner set, for each search.
func (s *Service) SearchSettings(ctx context.Context) (websearch.Settings, error) {
	rows, err := store.New(s.db).ListGatewaySearchProviders(ctx)
	if err != nil {
		return websearch.Settings{}, fmt.Errorf("reading the search providers' settings: %w", err)
	}

	var out websearch.Settings

	for _, r := range rows {
		if r.Off != 0 {
			out.Off = append(out.Off, r.Provider)
		}

		if r.Provider == websearch.ProviderSearXNG && r.BaseUrl.Valid {
			out.SearXNGURL = r.BaseUrl.String
		}
	}

	return out, nil
}

// SetSearchProvider switches a search provider on or off.
func (s *Service) SetSearchProvider(ctx context.Context, provider string, on bool) error {
	if !slices.Contains(websearch.Providers(), provider) {
		return fmt.Errorf("%w: no search provider %q", ErrNotFound, provider)
	}

	off := int64(1)
	if on {
		off = 0
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewaySearchOff(ctx, store.SetGatewaySearchOffParams{
			Provider: provider, Off: off, Now: now,
		}); err != nil {
			return fmt.Errorf("switching search provider %s: %w", provider, err)
		}

		return nil
	})
}

// SetSearXNG keeps the address of the owner's SearXNG; "" forgets it, and
// SearXNG is not asked.
func (s *Service) SetSearXNG(ctx context.Context, address string) error {
	stored := sql.NullString{}

	if address != "" {
		checked, err := websearch.SearXNGAddress(address)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		stored = sql.NullString{String: checked, Valid: true}
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewaySearchBaseURL(ctx, store.SetGatewaySearchBaseURLParams{
			Provider: websearch.ProviderSearXNG, BaseUrl: stored, Now: now,
		}); err != nil {
			return fmt.Errorf("keeping SearXNG's address: %w", err)
		}

		return nil
	})
}
