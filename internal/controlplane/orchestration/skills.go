package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// Skills (issue #21): a skill the owner adds is kept whole in revisions that
// never change, after Omnara's skill revisions; the same folder added again
// makes no new revision, as OpenClaw's identical saves.

// SkillUpload is where a skill given as an archive came from.
const SkillUpload = "upload"

// Skill is a skill at its newest revision; Revisions are every one, newest
// first, when the skill was read alone.
type Skill struct {
	Name string
	// Everyone gives it to every worker and Bot that does not turn it off.
	Everyone  bool
	Latest    SkillRevision
	Revisions []SkillRevision
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SkillRevision is one revision of a skill.
type SkillRevision struct {
	Revision    int
	Description string
	Digest      string
	Size        int64
	Files       int
	// Source is "upload" or "github:owner/repo[/path]@ref"; Commit is the
	// commit the ref was, for a repository.
	Source    string
	Commit    string
	CreatedAt time.Time
}

// SkillSource says where a skill came from.
type SkillSource struct {
	Source string
	Commit string
}

// AddSkill keeps a skill as its next revision, or, when its files are those
// of its newest one, keeps nothing and says so.
func (s *Service) AddSkill(ctx context.Context, sk skillpack.Skill, from SkillSource) (Skill, bool, error) {
	archive, err := sk.Archive()
	if err != nil {
		return Skill{}, false, fmt.Errorf("packing skill %s: %w", sk.Name, err)
	}

	added := false

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertSkill(ctx, store.InsertSkillParams{Name: sk.Name, Now: now}); err != nil {
			return err
		}

		latest, err := q.LatestSkillRevision(ctx, sk.Name)

		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case latest.Digest == sk.Digest:
			return nil
		}

		added = true

		if err := q.InsertSkillRevision(ctx, store.InsertSkillRevisionParams{
			Skill: sk.Name, Revision: latest.Revision + 1, Description: sk.Description, Digest: sk.Digest,
			Size: sk.Size(), Files: int64(len(sk.Files)), Source: from.Source, SourceCommit: nullString(from.Commit),
			Archive: archive, Now: now,
		}); err != nil {
			return err
		}

		return q.TouchSkill(ctx, store.TouchSkillParams{Name: sk.Name, Now: now})
	}); err != nil {
		return Skill{}, false, fmt.Errorf("keeping skill %s: %w", sk.Name, err)
	}

	got, err := s.GetSkill(ctx, sk.Name)

	return got, added, err
}

// ListSkills returns every skill at its newest revision, by name.
func (s *Service) ListSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.read().ListSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}

	out := make([]Skill, len(rows))
	for i, r := range rows {
		out[i] = Skill{
			Name: r.Name, Everyone: r.Everyone == 1, CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
			Latest: SkillRevision{
				Revision: int(r.Revision), Description: r.Description, Digest: r.Digest, Size: r.Size,
				Files: int(r.Files), Source: r.Source, Commit: r.SourceCommit.String, CreatedAt: millis(r.RevisedAt),
			},
		}
	}

	return out, nil
}

// GetSkill returns a skill with every revision.
func (s *Service) GetSkill(ctx context.Context, name string) (Skill, error) {
	q := s.read()

	row, err := q.GetSkill(ctx, name)
	if err != nil {
		return Skill{}, notFound(err, "skill", name)
	}

	revs, err := q.ListSkillRevisions(ctx, name)
	if err != nil {
		return Skill{}, fmt.Errorf("listing the revisions of skill %s: %w", name, err)
	}

	out := Skill{
		Name: row.Name, Everyone: row.Everyone == 1, CreatedAt: millis(row.CreatedAt), UpdatedAt: millis(row.UpdatedAt),
		Revisions: make([]SkillRevision, len(revs)),
	}

	for i, r := range revs {
		out.Revisions[i] = SkillRevision{
			Revision: int(r.Revision), Description: r.Description, Digest: r.Digest, Size: r.Size, Files: int(r.Files),
			Source: r.Source, Commit: r.SourceCommit.String, CreatedAt: millis(r.CreatedAt),
		}
	}

	if len(out.Revisions) > 0 {
		out.Latest = out.Revisions[0]
	}

	return out, nil
}

// SetSkillEveryone gives a skill to every worker and Bot, or takes it back.
func (s *Service) SetSkillEveryone(ctx context.Context, name string, on bool) (Skill, error) {
	every := int64(0)
	if on {
		every = 1
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetSkillEveryone(ctx, store.SetSkillEveryoneParams{Everyone: every, Now: now, Name: name})
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: skill %s", ErrNotFound, name)
		}

		return nil
	}); err != nil {
		return Skill{}, fmt.Errorf("setting skill %s for everyone: %w", name, err)
	}

	return s.GetSkill(ctx, name)
}

// DeleteSkill removes a skill and its revisions. Nothing names a skill yet;
// a Profile or a Bot that does will keep it (issue #21).
func (s *Service) DeleteSkill(ctx context.Context, name string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteSkill(ctx, name)
		if err != nil {
			return fmt.Errorf("removing skill %s: %w", name, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: skill %s", ErrNotFound, name)
		}

		return nil
	})
}
