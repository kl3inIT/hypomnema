package videos

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// Revisions made from another (issue #908, row 4; #937): a look change draws a made revision again in another look,
// and a retry makes a failed revision again as it was asked. Both number the new revision after the video's last,
// with the revision they start from as its parent, which is left as it is; both copy its lines, aspect, speed and
// captions on or off, and pin its voice, so that its clips and their word times are reused from the cache and no
// other provider or voice is asked (aidemo reuses a scene's voice and captions when only the look changes: AGENTS.md;
// src/voice.ts:320-372).

// LookChange asks for a revision in another look.
type LookChange struct {
	// Look is the sections sent; the others are the revision's.
	Look *LookInput
	// By and RequestID make the request safe to repeat, as a make's.
	By        string
	RequestID string
}

// ChangeLook queues a new revision of a made one (in review, approved or changes_requested) in another look. Its
// voice is pinned to the provider and voice the revision was spoken with.
func (s *Service) ChangeLook(ctx context.Context, id string, rev int, req LookChange) (Video, Revision, error) {
	if req.By == "" {
		req.By = Owner
	}

	if req.Look == nil {
		req.Look = &LookInput{}
	}

	if _, err := resolveLook(req.Look, DefaultLook()); err != nil {
		return Video{}, Revision{}, err
	}

	if err := checkRequestID(req.RequestID); err != nil {
		return Video{}, Revision{}, err
	}

	hash := requestHash("look", id, strconv.Itoa(rev), req.Look.canonical())

	return s.remake(ctx, id, rev, req.By, req.RequestID, hash, func(q *store.Queries, src store.VideoRevision,
		take devicetool.Take,
	) (newRevision, error) {
		if src.State != Review && src.State != Approved && src.State != ChangesRequested {
			return newRevision{}, fmt.Errorf("%w: revision %d is %s; the look is changed of a made one", ErrConflict,
				rev, src.State)
		}

		from, err := parseLook(src.Look)
		if err != nil {
			return newRevision{}, err
		}

		look, err := resolveLook(req.Look, from)
		if err != nil {
			return newRevision{}, err
		}

		if look, err = fitTake(look, req.Look.Frame != nil, take, src.Aspect); err != nil {
			return newRevision{}, err
		}

		if look, err = fitMusic(ctx, q, look, req.Look.Music != nil); err != nil {
			return newRevision{}, err
		}

		return newRevision{look: look.encode(), pin: &voicePin{
			provider: src.Provider, voice: src.UsedVoice, model: src.Model,
		}}, nil
	})
}

// Retry queues a failed revision again: its lines, look and voice as it was asked, never the latest lines, the saved
// default or the owner's providers' order now. A revision that was voiced before it failed is spoken by the same
// provider with the same voice, so no clip is asked again.
func (s *Service) Retry(ctx context.Context, id string, rev int, by, requestID string) (Video, Revision, error) {
	if by == "" {
		by = Owner
	}

	if err := checkRequestID(requestID); err != nil {
		return Video{}, Revision{}, err
	}

	hash := requestHash("retry", id, strconv.Itoa(rev))

	return s.remake(ctx, id, rev, by, requestID, hash, func(q *store.Queries, src store.VideoRevision,
		_ devicetool.Take,
	) (newRevision, error) {
		if src.State != Failed {
			return newRevision{}, fmt.Errorf("%w: revision %d is %s; only a failed one is made again", ErrConflict,
				rev, src.State)
		}

		// Its look as stored, its music fallen back to none if its track was deleted (issue #910, row 16).
		look, err := fitStoredMusic(ctx, q, src.Look)
		if err != nil {
			return newRevision{}, err
		}

		out := newRevision{look: look}

		switch {
		case src.PinProvider != "":
			// A pin made before pins kept their model has none: the model the revision was spoken with stands for
			// it, before the default (review of #1300).
			out.pin = &voicePin{provider: src.PinProvider, voice: src.PinVoice, model: cmp.Or(src.PinModel, src.Model)}
		case src.Provider != "":
			out.pin = &voicePin{provider: src.Provider, voice: src.UsedVoice, model: src.Model}
		}

		return out, nil
	})
}

// checkRequestID refuses a look change or a retry without a request id (row 4): sent twice, it would queue two
// revisions.
func checkRequestID(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("%w: a look change or a retry needs a request id (an Idempotency-Key)", ErrInvalid)
	case len(id) > MaxRequestID:
		return fmt.Errorf("%w: a request id is at most %d bytes", ErrInvalid, MaxRequestID)
	}

	return nil
}

// remake queues a revision made from revision rev, as plan says from it and the take; the lines, voice, captions
// and aspect are rev's.
func (s *Service) remake(ctx context.Context, id string, rev int, by, requestID, hash string,
	plan func(q *store.Queries, src store.VideoRevision, take devicetool.Take) (newRevision, error),
) (Video, Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if v, r, ok, err := s.byRequest(ctx, by, requestID, hash); ok || err != nil {
		return v, r, err
	}

	v, err := s.Get(ctx, id)
	if err != nil {
		return Video{}, Revision{}, err
	}

	take, err := s.cfg.Takes.Get(v.Take)
	if err != nil {
		return Video{}, Revision{}, fmt.Errorf("%w: the take was deleted", ErrConflict)
	}

	if take.State != devicetool.TakeAvailable {
		return Video{}, Revision{}, fmt.Errorf("%w: take %s is %s, not available", ErrConflict, take.ID, take.State)
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		src, err := q.GetRevision(ctx, store.GetRevisionParams{VideoID: id, Rev: int64(rev)})

		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: revision %d of %s", ErrNotFound, rev, id)
		case err != nil:
			return err
		}

		r, err := plan(q, src, take)
		if err != nil {
			return err
		}

		if n, err := q.CountQueued(ctx); err != nil {
			return err
		} else if n >= MaxQueued {
			return fmt.Errorf("%w: %d revisions are queued already; try again once one is made", ErrConflict, n)
		}

		if err := json.Unmarshal([]byte(src.Lines), &r.lines); err != nil {
			return err
		}

		r.parent = sql.NullInt64{Int64: int64(rev), Valid: true}
		r.voice, r.speed, r.captions, r.aspect = src.Voice, src.Speed, src.Captions != 0, src.Aspect
		r.by, r.requestID, r.hash = by, requestID, hash

		return s.insertRevision(ctx, q, now, id, r)
	})
	if err != nil {
		return Video{}, Revision{}, err
	}

	s.poke()

	v, err = s.Get(ctx, id)
	if err != nil {
		return Video{}, Revision{}, err
	}

	return v, v.Latest(), nil
}

// Catalog is what a look is chosen from (issue #908, row 15): the caption presets, Classic included, the
// backgrounds, the chromes, and the saved default.
type Catalog struct {
	Presets     []PresetInfo     `json:"presets"`
	Backgrounds []string         `json:"backgrounds"`
	Chromes     []compose.Chrome `json:"chromes"`
	Default     Look             `json:"default"`
}

// PresetInfo is a caption preset as offered.
type PresetInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Looks is the catalog of looks.
func (s *Service) Looks(ctx context.Context) (Catalog, error) {
	def, err := s.savedLook(ctx, store.New(s.db))
	if err != nil {
		return Catalog{}, err
	}

	out := Catalog{
		Backgrounds: append([]string{}, compose.Backgrounds...), Chromes: append([]compose.Chrome{}, compose.Chromes...),
		Default: def,
	}

	for _, p := range captions.Presets {
		out.Presets = append(out.Presets, PresetInfo{ID: p.ID, Name: p.Name})
	}

	return out, nil
}
