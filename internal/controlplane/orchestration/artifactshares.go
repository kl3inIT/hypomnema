package orchestration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// View-only links to an artifact (issue #745): anyone who has one sees the artifact's newest revision without
// signing in, until the owner revokes it. A link is a row with a random token, as open-webui's shared_chat,
// LibreChat's SharedLink and Grafana's public dashboard are; it names the artifact, not a revision, so it follows
// the newest as OpenMausBot's published pages and Claude's do. Revoking deletes the row (open-webui, LibreChat), and
// deleting the artifact with its chat deletes its links (ON DELETE CASCADE, migration 00105).

// ArtifactShareIDPrefix starts every link's id.
const ArtifactShareIDPrefix = "shr"

// maxShareLabel bounds a link's label, the name of whom it was sent to.
const maxShareLabel = 80

// ArtifactShare is a view-only link to an artifact.
type ArtifactShare struct {
	ID         string
	ArtifactID string
	// Token is the link's secret, 32 random bytes in base64url (the frame token's size).
	Token        string
	Label        string
	CreatedAt    time.Time
	Views        int64
	LastViewedAt *time.Time
}

// SharedArtifact is what a link shows: its artifact at the newest revision.
type SharedArtifact struct {
	ShareID    string
	ArtifactID string
	Revision   int
	Title      string
	Type       artifactpack.Type
	Entry      string
	// MediaType is the type of the file shown.
	MediaType string
	UpdatedAt time.Time
}

// CreateArtifactShare makes a new link to an artifact, labeled for whom it is sent to.
func (s *Service) CreateArtifactShare(ctx context.Context, artifactID, label string) (ArtifactShare, error) {
	label = strings.TrimSpace(label)
	if label == "" || utf8.RuneCountInString(label) > maxShareLabel || strings.ContainsAny(label, "\r\n") {
		return ArtifactShare{}, fmt.Errorf("%w: a link's label is one line of 1 to %d characters", ErrInvalid,
			maxShareLabel)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ArtifactShare{}, fmt.Errorf("making a link's token: %w", err)
	}

	out := ArtifactShare{
		ID: s.newID(ArtifactShareIDPrefix), ArtifactID: artifactID, Token: base64.RawURLEncoding.EncodeToString(raw),
		Label: label,
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetArtifact(ctx, artifactID); err != nil {
			return notFound(err, "artifact", artifactID)
		}

		out.CreatedAt = millis(now)

		return q.InsertArtifactShare(ctx, store.InsertArtifactShareParams{
			ID: out.ID, ArtifactID: artifactID, Token: out.Token, Label: label, Now: now,
		})
	})
	if err != nil {
		return ArtifactShare{}, fmt.Errorf("sharing artifact %s: %w", artifactID, err)
	}

	return out, nil
}

// ListArtifactShares returns an artifact's links, newest first.
func (s *Service) ListArtifactShares(ctx context.Context, artifactID string) ([]ArtifactShare, error) {
	q := s.read()

	if _, err := q.GetArtifact(ctx, artifactID); err != nil {
		return nil, notFound(err, "artifact", artifactID)
	}

	rows, err := q.ListArtifactShares(ctx, artifactID)
	if err != nil {
		return nil, fmt.Errorf("listing the links of artifact %s: %w", artifactID, err)
	}

	out := make([]ArtifactShare, 0, len(rows))
	for _, r := range rows {
		out = append(out, ArtifactShare{
			ID: r.ID, ArtifactID: r.ArtifactID, Token: r.Token, Label: r.Label, CreatedAt: millis(r.CreatedAt),
			Views: r.Views, LastViewedAt: optionalTime(r.LastViewedAt),
		})
	}

	return out, nil
}

// DeleteArtifactShare revokes a link: the next request by its token finds nothing.
func (s *Service) DeleteArtifactShare(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteArtifactShare(ctx, id)
		if err != nil {
			return fmt.Errorf("revoking link %s: %w", id, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: link %s", ErrNotFound, id)
		}

		return nil
	})
}

// SharedArtifact returns what a link's token shows; ErrNotFound for a token no link has.
func (s *Service) SharedArtifact(ctx context.Context, token string) (SharedArtifact, error) {
	r, err := s.read().SharedArtifact(ctx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return SharedArtifact{}, fmt.Errorf("%w: no such link", ErrNotFound)
	}

	if err != nil {
		return SharedArtifact{}, fmt.Errorf("reading a link: %w", err)
	}

	return SharedArtifact{
		ShareID: r.ID, ArtifactID: r.ArtifactID, Revision: int(r.Revision), Title: r.Title,
		Type: artifactpack.Type(r.Type), Entry: r.Entry, MediaType: r.MediaType, UpdatedAt: millis(r.UpdatedAt),
	}, nil
}

// CountArtifactShareView counts one opening of a link's page and when it was.
func (s *Service) CountArtifactShareView(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.CountArtifactShareView(ctx, store.CountArtifactShareViewParams{
			ID: id, Now: sql.NullInt64{Int64: now, Valid: true},
		})
	})
}
