package modelgateway

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

// What the owner sets on Settings → Gateway → Images (issue #431), kept as
// web search's switches and order are (search_switches.go): a switch for each
// image provider, all on until turned off; the order they are asked in, by
// default Codex then Antigravity (owner, 2026-11-14); and the model each
// makes images with when a request names none, by default its reference's,
// which the owner may change to try a newer one (owner, 2026-11-14). Read at
// each request, so a change counts from the next.

// ImageProviderSetting is one image provider as the owner set it.
type ImageProviderSetting struct {
	Provider string
	On       bool
	// Model is the model it makes images with when a request names none:
	// the owner's, else DefaultImageModel.
	Model string
	// Chosen says the owner chose Model.
	Chosen bool
}

// ImageSettings are the image providers in the order they are asked.
func (s *Service) ImageSettings(ctx context.Context) ([]ImageProviderSetting, error) {
	rows, err := store.New(s.db).ListGatewayImageProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the image providers' settings: %w", err)
	}

	stored := map[string]store.GatewayImageProvider{}
	for _, r := range rows {
		stored[r.Provider] = r
	}

	providers := ImageProviders()
	defaults := map[string]int{}

	for i, p := range providers {
		defaults[p] = i
	}

	// Those the owner placed first, by their place; the rest after, in the default order.
	slices.SortStableFunc(providers, func(a, b string) int {
		pa, pb := stored[a].Position, stored[b].Position

		switch {
		case pa.Valid && pb.Valid:
			return cmp.Or(cmp.Compare(pa.Int64, pb.Int64), cmp.Compare(defaults[a], defaults[b]))
		case pa.Valid:
			return -1
		case pb.Valid:
			return 1
		default:
			return cmp.Compare(defaults[a], defaults[b])
		}
	})

	out := make([]ImageProviderSetting, 0, len(providers))

	for _, p := range providers {
		r := stored[p]
		setting := ImageProviderSetting{Provider: p, On: r.Off == 0, Model: DefaultImageModel(p)}

		if r.Model.Valid {
			setting.Model, setting.Chosen = r.Model.String, true
		}

		out = append(out, setting)
	}

	return out, nil
}

func checkImageProvider(provider string) error {
	if !slices.Contains(ImageProviders(), provider) {
		return fmt.Errorf("%w: no image provider %q", ErrNotFound, provider)
	}

	return nil
}

// SetImageProvider switches an image provider on or off.
func (s *Service) SetImageProvider(ctx context.Context, provider string, on bool) error {
	if err := checkImageProvider(provider); err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewayImageOff(ctx, store.SetGatewayImageOffParams{
			Provider: provider, Off: flag(!on), Now: now,
		}); err != nil {
			return fmt.Errorf("switching image provider %s: %w", provider, err)
		}

		return nil
	})
}

// SetImageModel keeps the model an image provider makes images with when a
// request names none; "" goes back to its default. Any well-formed name is
// kept: whether the provider has it, its first answer says.
func (s *Service) SetImageModel(ctx context.Context, provider, model string) error {
	if err := checkImageProvider(provider); err != nil {
		return err
	}

	if model != "" && !ValidImageModel(model) {
		return fmt.Errorf("%w: %q is not a model's name: letters, digits and . _ : -", ErrInvalid, model)
	}

	stored := sql.NullString{String: model, Valid: model != "" && model != DefaultImageModel(provider)}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewayImageModel(ctx, store.SetGatewayImageModelParams{
			Provider: provider, Model: stored, Now: now,
		}); err != nil {
			return fmt.Errorf("setting image provider %s's model: %w", provider, err)
		}

		return nil
	})
}

// SetImageOrder keeps the order the image providers are asked in: each once.
func (s *Service) SetImageOrder(ctx context.Context, providers []string) error {
	all := ImageProviders()
	if len(providers) != len(all) || !sameSet(providers, all) {
		return fmt.Errorf("%w: the order names each image provider once: %v", ErrInvalid, all)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		for i, p := range providers {
			if err := q.SetGatewayImagePosition(ctx, store.SetGatewayImagePositionParams{
				Provider: p, Position: sql.NullInt64{Int64: int64(i + 1), Valid: true}, Now: now,
			}); err != nil {
				return fmt.Errorf("placing image provider %s: %w", p, err)
			}
		}

		return nil
	})
}

// ImageTally is how many image requests a provider answered and failed.
type ImageTally struct{ Answered, Failed int }

// ImageCount is a provider's image requests of the UTC day and month.
type ImageCount struct {
	Today, Month ImageTally
}

// ImageCounts are each provider's image requests of the UTC day and month
// now is in, from the usage rows (an image call is one).
func (s *Service) ImageCounts(ctx context.Context, now time.Time) (map[string]ImageCount, error) {
	q := store.New(s.db)
	month := monthStart(now)
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)

	out := map[string]ImageCount{}

	for _, period := range []struct {
		since time.Time
		set   func(c *ImageCount, t ImageTally)
	}{
		{month, func(c *ImageCount, t ImageTally) { c.Month = t }},
		{day, func(c *ImageCount, t ImageTally) { c.Today = t }},
	} {
		rows, err := q.CountGatewayImageCalls(ctx, period.since.UnixMilli())
		if err != nil {
			return nil, fmt.Errorf("counting the image calls: %w", err)
		}

		for _, r := range rows {
			c := out[r.Provider]
			period.set(&c, ImageTally{Answered: int(r.Answered), Failed: int(r.Failed)})
			out[r.Provider] = c
		}
	}

	return out, nil
}
