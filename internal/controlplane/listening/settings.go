package listening

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening/internal/store"
)

// What the owner sets of the sources on Settings → Gateway → Social listening (issue #1290): a switch for each source
// and the order of its backends, kept beside the web search's switches and order (modelgateway/search_switches.go).
// A source is on until the owner turns it off, every source here being keyless (last30days KEYLESS_ALWAYS_ON,
// doctor.py:119-126, and its setup's "Default: on, no question", research note §5b); a source that is off is neither
// read nor may a plan name it, as a web search provider that is off (SetSearchProvider).

// ErrNotFound is the error of a source that does not exist.
var ErrNotFound = errors.New("not found")

// settings are the owner's switches and orders, as kept.
type settings struct {
	off   map[string]bool
	order map[string][]string
}

func (s *Service) settings(ctx context.Context) (settings, error) {
	out := settings{off: map[string]bool{}, order: map[string][]string{}}
	if s.db == nil {
		return out, nil
	}

	q := store.New(s.db)

	sources, err := q.ListListeningSources(ctx)
	if err != nil {
		return settings{}, fmt.Errorf("reading the listening sources' switches: %w", err)
	}

	for _, r := range sources {
		out.off[r.Source] = r.Off != 0
	}

	backends, err := q.ListListeningBackends(ctx)
	if err != nil {
		return settings{}, fmt.Errorf("reading the listening backends' order: %w", err)
	}

	placed := map[string][]store.ListeningBackend{}

	for _, r := range backends {
		if r.Position.Valid {
			placed[r.Source] = append(placed[r.Source], r)
		}
	}

	for source, rows := range placed {
		slices.SortFunc(rows, func(a, b store.ListeningBackend) int {
			return cmp.Or(cmp.Compare(a.Position.Int64, b.Position.Int64), cmp.Compare(a.Backend, b.Backend))
		})

		for _, r := range rows {
			out.order[source] = append(out.order[source], r.Backend)
		}
	}

	return out, nil
}

// ordered is a source's backends in the owner's order; a backend the order does not name keeps its place after
// those it names, in the registry's order.
func (p Platform) ordered(order []string) Platform {
	if len(order) == 0 {
		return p
	}

	rank := func(b Backend) int {
		if i := slices.Index(order, b.Name); i >= 0 {
			return i
		}

		return len(order)
	}

	out := p
	out.Backends = slices.Clone(p.Backends)
	slices.SortStableFunc(out.Backends, func(a, b Backend) int { return cmp.Compare(rank(a), rank(b)) })

	return out
}

// live is the sources as the owner set them: each in the registry's order, its backends in his, and whether it is on.
func (s *Service) live(ctx context.Context) ([]Platform, map[string]bool, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, nil, err
	}

	out := make([]Platform, 0, len(s.platforms))
	on := map[string]bool{}

	for _, p := range s.platforms {
		out = append(out, p.ordered(set.order[p.Name]))
		on[p.Name] = !set.off[p.Name]
	}

	return out, on, nil
}

// On are the sources a plan may name now, in order: those the owner has not switched off.
func (s *Service) On(ctx context.Context) ([]string, error) {
	platforms, on, err := s.live(ctx)
	if err != nil {
		return nil, err
	}

	var out []string

	for _, p := range platforms {
		if on[p.Name] {
			out = append(out, p.Name)
		}
	}

	return out, nil
}

// SetSource switches a source on or off.
func (s *Service) SetSource(ctx context.Context, source string, on bool) error {
	if s.platform(source).Name == "" {
		return fmt.Errorf("%w: no listening source %q", ErrNotFound, source)
	}

	if s.db == nil {
		return fmt.Errorf("%w: this control plane keeps no listening settings", ErrInvalid)
	}

	off := int64(1)
	if on {
		off = 0
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.SetListeningSourceOff(ctx, store.SetListeningSourceOffParams{Source: source, Off: off, Now: now})
	})
}

// SetOrder keeps the order a source tries its backends in: each of them once.
func (s *Service) SetOrder(ctx context.Context, source string, backends []string) error {
	p := s.platform(source)

	switch {
	case p.Name == "":
		return fmt.Errorf("%w: no listening source %q", ErrNotFound, source)
	case s.db == nil:
		return fmt.Errorf("%w: this control plane keeps no listening settings", ErrInvalid)
	case len(backends) != len(p.Backends) || !sameSet(backends, p.backendNames()):
		return fmt.Errorf("%w: the order names each of %s's backends once: %v", ErrInvalid, source,
			p.backendNames())
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		for i, b := range backends {
			if err := q.SetListeningBackendPosition(ctx, store.SetListeningBackendPositionParams{
				Source: source, Backend: b, Position: sql.NullInt64{Int64: int64(i + 1), Valid: true}, Now: now,
			}); err != nil {
				return fmt.Errorf("placing %s's backend %s: %w", source, b, err)
			}
		}

		return nil
	})
}

func sameSet(a, b []string) bool {
	x, y := slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))

	return slices.Equal(slices.Compact(x), y)
}

// write runs fn in a transaction, with the time now in milliseconds, as the gateway writes its own
// (modelgateway.Service.write).
func (s *Service) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(store.New(tx), s.now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	return nil
}

// at is a kept time in milliseconds as a time; zero when it is not kept.
func at(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}

	return time.UnixMilli(v.Int64).UTC()
}

// switchedOff says the owner has switched a source off by now: a search under way starts no new read of it (review
// round 1 of PR #1322), as a web search provider switched off is not tried from the next call on. A switch that
// cannot be read is taken as on: the search began with it on.
func (s *Service) switchedOff(ctx context.Context, source string) bool {
	if s.db == nil {
		return false
	}

	n, err := store.New(s.db).ListeningSourceIsOff(context.WithoutCancel(ctx), source)
	if err != nil {
		s.logger.WarnContext(ctx, "a listening source's switch could not be read", slog.String("event",
			"listening.switch_failed"), slog.String("source", source), slog.String("error", err.Error()))

		return false
	}

	return n > 0
}
