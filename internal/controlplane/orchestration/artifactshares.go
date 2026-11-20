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

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// View-only links to an artifact (issue #745): anyone who has one sees the artifact's newest revision without
// signing in, until the owner revokes it. A link is a row with a random token, as open-webui's shared_chat,
// LibreChat's SharedLink and Grafana's public dashboard are; it names the artifact, not a revision, so it follows
// the newest as OpenMausBot's published pages and Claude's do. Its token is kept as a credential is (issue #996):
// its SHA-256 finds it, and a copy sealed in the vault lets the owner read the link again, as an owner-made gateway
// key's value is kept (modelgateway key_values.go); nothing keeps it in plaintext. Revoking marks the row with the
// reason and drops the sealed copy, and deleting the artifact with its chat deletes its links (ON DELETE CASCADE)
// and their copies.

// ArtifactShareIDPrefix starts every link's id.
const ArtifactShareIDPrefix = "shr"

// maxShareLabel bounds a link's label, the name of whom it was sent to.
const maxShareLabel = 80

// ArtifactShare is a view-only link to an artifact.
type ArtifactShare struct {
	ID         string
	ArtifactID string
	// Token is the link's secret, 32 random bytes in base64url (the frame token's size), opened from its sealed
	// copy; empty when it has none (made without a vault) or the vault cannot open it here.
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

	err := s.writeTx(ctx, func(tx *sql.Tx, q *store.Queries, now int64) error {
		if _, err := q.GetArtifact(ctx, artifactID); err != nil {
			return notFound(err, "artifact", artifactID)
		}

		out.CreatedAt = millis(now)

		versionID, err := s.sealShareToken(ctx, tx, out.ID, out.Token, now)
		if err != nil {
			return err
		}

		return q.InsertArtifactShare(ctx, store.InsertArtifactShareParams{
			ID: out.ID, ArtifactID: artifactID, TokenHash: nullString(bearertoken.Hash(out.Token)),
			SecretVersionID: nullString(versionID), Label: label, Now: now,
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
		token, err := s.openShareToken(ctx, r.ID, r.Token, r.TokenHash, r.SecretVersionID)
		if err != nil {
			return nil, err
		}

		out = append(out, ArtifactShare{
			ID: r.ID, ArtifactID: r.ArtifactID, Token: token, Label: r.Label, CreatedAt: millis(r.CreatedAt),
			Views: r.Views, LastViewedAt: optionalTime(r.LastViewedAt),
		})
	}

	return out, nil
}

// SharedLink is a link with what it shows: its artifact's newest title and type, its Bot and its chat.
type SharedLink struct {
	ArtifactShare

	Title string
	Type  artifactpack.Type
	Bot   string
	// BotName is the Bot's name as the owner reads it; empty when the Bot is gone.
	BotName string
	ChatID  string
}

// ListSharedLinks returns every link, newest first (issue #765): the owner's one place to see what is shared and
// with whom, as Open WebUI's and LibreChat's shared-links lists and Grafana's public dashboards list are.
func (s *Service) ListSharedLinks(ctx context.Context) ([]SharedLink, error) {
	rows, err := s.read().ListAllArtifactShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the links: %w", err)
	}

	out := make([]SharedLink, 0, len(rows))
	for _, r := range rows {
		token, err := s.openShareToken(ctx, r.ID, r.Token, r.TokenHash, r.SecretVersionID)
		if err != nil {
			return nil, err
		}

		out = append(out, SharedLink{
			ArtifactShare: ArtifactShare{
				ID: r.ID, ArtifactID: r.ArtifactID, Token: token, Label: r.Label, CreatedAt: millis(r.CreatedAt),
				Views: r.Views, LastViewedAt: optionalTime(r.LastViewedAt),
			},
			Title: r.Title, Type: artifactpack.Type(r.Type), Bot: r.BotID, BotName: r.BotName,
			ChatID: r.ChatID,
		})
	}

	return out, nil
}

// DeleteArtifactShare revokes a link: the next request by its token finds nothing. The link is kept, revoked by the
// owner (issue #996), and its sealed copy is deleted; one revoked already is not found.
func (s *Service) DeleteArtifactShare(ctx context.Context, id string) error {
	return s.writeTx(ctx, func(tx *sql.Tx, q *store.Queries, now int64) error {
		row, err := q.GetLiveArtifactShare(ctx, id)
		if err != nil {
			return notFound(err, "link", id)
		}

		hash := row.TokenHash
		if !hash.Valid {
			hash = nullString(bearertoken.Hash(row.Token.String))
		}

		if _, err := q.RevokeArtifactShare(ctx, store.RevokeArtifactShareParams{
			Now: validInt(now), Reason: revokeReason(bearertoken.RevokedByOwner), TokenHash: hash, ID: id,
		}); err != nil {
			return fmt.Errorf("revoking link %s: %w", id, err)
		}

		return s.deleteShareSecrets(ctx, tx, []sql.NullString{row.SecretVersionID})
	})
}

// ShareTokenMove is what MoveShareTokens did: the links it moved, and those whose plaintext stays until a vault can
// seal it.
type ShareTokenMove struct {
	Moved   int
	Waiting int
}

// MoveShareTokens moves the tokens of links made before they were hashed (issue #996) out of plaintext: each gets
// its hash and a sealed copy and loses its plaintext, in one commit, so its URL keeps working and the owner can still
// read it. Without a vault nothing is moved: a link keeps its plaintext, which still opens it, until a vault can seal
// it, since the owner could never read it again otherwise. The control plane calls it as it starts, before anything
// is served.
//
// What a deleted or rewritten row held stays in the file's free pages and in the write-ahead log (SQLite's
// secure_delete is off), and the migration's rebuild of the table left the plaintext there too. So the move zeroes
// what it rewrites (secure_delete on), and after it the database is rebuilt (VACUUM) and the log written back and
// emptied (wal_checkpoint(TRUNCATE), as sqlitetest's template does): no plaintext token is left in the files.
func (s *Service) MoveShareTokens(ctx context.Context) (ShareTokenMove, error) {
	var out ShareTokenMove

	rows, err := s.read().ListUnmovedArtifactShares(ctx)
	if err != nil {
		return out, fmt.Errorf("listing the links to move: %w", err)
	}

	if len(rows) == 0 {
		return out, nil
	}

	if !s.shareVaultReady() {
		out.Waiting = len(rows)

		return out, nil
	}

	if _, err := s.db.ExecContext(ctx, "PRAGMA secure_delete = ON"); err != nil {
		return out, fmt.Errorf("zeroing what the move rewrites: %w", err)
	}

	defer s.db.ExecContext(context.WithoutCancel(ctx), "PRAGMA secure_delete = OFF") //nolint:errcheck // best effort

	err = s.writeTx(ctx, func(tx *sql.Tx, q *store.Queries, now int64) error {
		rows, err := q.ListUnmovedArtifactShares(ctx)
		if err != nil {
			return fmt.Errorf("listing the links to move: %w", err)
		}

		for _, r := range rows {
			versionID, err := s.sealShareToken(ctx, tx, r.ID, r.Token.String, now)
			if err != nil {
				return err
			}

			if err := q.MoveArtifactShareToken(ctx, store.MoveArtifactShareTokenParams{
				TokenHash: nullString(bearertoken.Hash(r.Token.String)), SecretVersionID: nullString(versionID),
				ID: r.ID,
			}); err != nil {
				return fmt.Errorf("moving the token of link %s: %w", r.ID, err)
			}
		}

		out.Moved = len(rows)

		return nil
	})
	if err != nil {
		return ShareTokenMove{}, fmt.Errorf("moving the links' tokens: %w", err)
	}

	for _, stmt := range []string{"VACUUM", "PRAGMA wal_checkpoint(TRUNCATE)"} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return out, fmt.Errorf("clearing the moved tokens from the database's files (%s): %w", stmt, err)
		}
	}

	return out, nil
}

// shareVaultReady says whether a vault can seal a link's token here.
func (s *Service) shareVaultReady() bool { return s.shareVault != nil && s.shareVault.Ready() }

// sealShareToken keeps a link's token in the vault in tx and returns the version's id. Without a vault that can seal
// it, it is ErrUnavailable: a link is never kept by its hash alone, which the owner could not read again.
func (s *Service) sealShareToken(ctx context.Context, tx *sql.Tx, id, token string, now int64) (string, error) {
	if !s.shareVaultReady() {
		return "", fmt.Errorf("%w: the vault is not configured, and a link is kept only with its sealed copy",
			ErrUnavailable)
	}

	versionID, err := s.shareVault.SealTx(ctx, tx, strings.ReplaceAll(id, "_", "-"), token, now)
	if err != nil {
		return "", fmt.Errorf("sealing the token of link %s: %w", id, err)
	}

	if versionID == "" {
		return "", fmt.Errorf("%w: the vault kept no copy of link %s", ErrUnavailable, id)
	}

	return versionID, nil
}

// openShareToken opens a link's sealed token, checked against its hash, as KeyValue checks a gateway key's; a link
// not moved yet reads its plaintext; "" for one the vault cannot open here.
func (s *Service) openShareToken(ctx context.Context, id string, plain, hash, versionID sql.NullString,
) (string, error) {
	if plain.Valid {
		return plain.String, nil
	}

	if !versionID.Valid || s.shareVault == nil {
		return "", nil
	}

	token, err := s.shareVault.Open(ctx, versionID.String)
	if err != nil {
		return "", fmt.Errorf("opening the token of link %s: %w", id, err)
	}

	if token != "" && bearertoken.Hash(token) != hash.String {
		return "", fmt.Errorf("the sealed token of link %s does not match its hash", id)
	}

	return token, nil
}

// deleteShareSecrets deletes the sealed copies of links that end, in tx.
func (s *Service) deleteShareSecrets(ctx context.Context, tx *sql.Tx, versionIDs []sql.NullString) error {
	for _, v := range versionIDs {
		if !v.Valid {
			continue
		}

		if s.shareVault == nil {
			return fmt.Errorf("%w: a link's sealed token cannot be deleted without the vault", ErrUnavailable)
		}

		if err := s.shareVault.DeleteTx(ctx, tx, v.String); err != nil {
			return fmt.Errorf("deleting a link's sealed token: %w", err)
		}
	}

	return nil
}

// SharesUsingSecret names the links whose sealed token is the secret: the vault lists them as its users and refuses
// to delete it while they work, as it does for a gateway key's (issue #893).
func (s *Service) SharesUsingSecret(ctx context.Context, name string) ([]string, error) {
	rows, err := s.read().ListShareLabelsBySecret(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("listing the links that use %q: %w", name, err)
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("link %s (%s)", r.ID, r.Label))
	}

	return out, nil
}

// SharedArtifact returns what a link's token shows; ErrNotFound for a token no working link has.
func (s *Service) SharedArtifact(ctx context.Context, token string) (SharedArtifact, error) {
	r, err := s.read().SharedArtifact(ctx, store.SharedArtifactParams{
		TokenHash: nullString(bearertoken.Hash(token)), Token: nullString(token),
	})
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

// CountArtifactShareView counts one opening of a link's page; when it was is written at most once an hour, as a
// credential's last use is (issue #996).
func (s *Service) CountArtifactShareView(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.CountArtifactShareView(ctx, store.CountArtifactShareViewParams{
			Now: now, Before: now - bearertoken.TouchEvery.Milliseconds(), ID: id,
		})
	})
}
