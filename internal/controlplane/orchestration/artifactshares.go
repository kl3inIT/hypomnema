package orchestration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
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
		token, err := s.openShareToken(ctx, r.ID, r.TokenHash, r.SecretVersionID)
		if err != nil {
			return nil, err
		}

		out = append(out, s.withUnwrittenViews(ArtifactShare{
			ID: r.ID, ArtifactID: r.ArtifactID, Token: token, Label: r.Label, CreatedAt: millis(r.CreatedAt),
			Views: r.Views, LastViewedAt: optionalTime(r.LastViewedAt),
		}))
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
		token, err := s.openShareToken(ctx, r.ID, r.TokenHash, r.SecretVersionID)
		if err != nil {
			return nil, err
		}

		out = append(out, SharedLink{
			ArtifactShare: s.withUnwrittenViews(ArtifactShare{
				ID: r.ID, ArtifactID: r.ArtifactID, Token: token, Label: r.Label, CreatedAt: millis(r.CreatedAt),
				Views: r.Views, LastViewedAt: optionalTime(r.LastViewedAt),
			}),
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

// ShareTokenMove is what MoveShareTokens did: the links it moved, and those whose plaintext waits for a vault that
// can seal it.
type ShareTokenMove struct {
	Moved   int
	Waiting int
}

// MoveShareTokens moves the tokens of links made before they were hashed (issue #996) out of plaintext: each gets
// its hash and a sealed copy and loses its plaintext, in one commit, so its URL keeps working and the owner can still
// read it. What the move rewrites is zeroed (secure_delete on). Without a vault that can seal them nothing is moved:
// such a link is neither opened nor read again from its plaintext, and keeps it only until a vault seals it, since
// the owner could never read it again otherwise. The control plane calls it as it starts, before anything is served,
// then ScrubShareTokens.
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

	return out, nil
}

// ScrubShareTokens clears the database's files of the old tokens' plaintext once every one is moved, and reports
// whether nothing is left to clear. What a deleted or rewritten row held stays in the file's free pages and in the
// write-ahead log (SQLite's secure_delete is off), and the migration's rebuild of the table left the plaintext there:
// the file is rebuilt (VACUUM) and the log written back and emptied (wal_checkpoint(TRUNCATE), as sqlitetest's
// template is). The checkpoint is not done while another connection reads an older snapshot (its busy column), and
// the mark of what is owed (share_token_scrub, set by the migration) is cleared only once it is; so a stop or a busy
// reader leaves it for the next try, and no link is served meanwhile (SharedArtifact).
func (s *Service) ScrubShareTokens(ctx context.Context) (bool, error) {
	q := s.read()

	pending, err := q.ShareScrubPending(ctx)
	if err != nil {
		return false, fmt.Errorf("reading what is owed to the database's files: %w", err)
	}

	if pending == 0 {
		return true, nil
	}

	unmoved, err := q.ListUnmovedArtifactShares(ctx)
	if err != nil {
		return false, fmt.Errorf("listing the links to move: %w", err)
	}

	if len(unmoved) > 0 {
		return false, nil
	}

	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return false, fmt.Errorf("rebuilding the database's file: %w", err)
	}

	if done, err := s.checkpoint(ctx); err != nil || !done {
		return false, err
	}

	if err := s.write(ctx, func(q *store.Queries, _ int64) error { return q.ClearShareScrub(ctx) }); err != nil {
		return false, fmt.Errorf("recording that the database's files are clear: %w", err)
	}

	// The clearing's own write holds no token; it is written back as well as the database allows.
	_, _ = s.checkpoint(ctx)

	return true, nil
}

// checkpoint writes the log back into the file and empties it, and reports whether it did: SQLite answers busy
// when a reader still holds an older snapshot.
func (s *Service) checkpoint(ctx context.Context) (bool, error) {
	var busy, logFrames, checkpointed int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames,
		&checkpointed); err != nil {
		return false, fmt.Errorf("writing the log back into the database's file: %w", err)
	}

	return busy == 0, nil
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

// openShareToken opens a link's sealed token, checked against its hash, as KeyValue checks a gateway key's; "" for
// a link not moved yet, whose plaintext is never read again, or one the vault cannot open here.
func (s *Service) openShareToken(ctx context.Context, id string, hash, versionID sql.NullString) (string, error) {
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

// SharedArtifact returns what a link's token shows; ErrNotFound for a token no working link has. While the
// database's files may still hold old tokens in plaintext (ScrubShareTokens), no link is served: ErrUnavailable.
func (s *Service) SharedArtifact(ctx context.Context, token string) (SharedArtifact, error) {
	q := s.read()

	pending, err := q.ShareScrubPending(ctx)
	if err != nil {
		return SharedArtifact{}, fmt.Errorf("reading a link: %w", err)
	}

	if pending > 0 {
		return SharedArtifact{}, fmt.Errorf("%w: links are served once the old links' tokens are cleared",
			ErrUnavailable)
	}

	r, err := q.SharedArtifact(ctx, nullString(bearertoken.Hash(token)))
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

// shareViewsWriteEvery bounds how often the openings of links are written: a link opened often is no write each
// time (issue #996), and openings counted since the last write are lost with the process, a count being a count.
const shareViewsWriteEvery = time.Minute

// shareViews are the openings of links counted since they were last written, kept in memory.
type shareViews struct {
	mu      sync.Mutex
	pending map[string]shareView
	written time.Time
}

// shareView is one link's openings since the last write and when the newest was, in milliseconds.
type shareView struct {
	count int64
	last  int64
}

func newShareViews() *shareViews { return &shareViews{pending: map[string]shareView{}} }

// unwritten returns a link's openings not written yet.
func (v *shareViews) unwritten(id string) shareView {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.pending[id]
}

// add counts back openings whose write failed.
func (v *shareViews) add(batch map[string]shareView) {
	v.mu.Lock()
	defer v.mu.Unlock()

	for id, b := range batch {
		p := v.pending[id]
		p.count += b.count
		p.last = max(p.last, b.last)
		v.pending[id] = p
	}
}

// CountArtifactShareView counts one opening of a link's page. Openings are written together at most once a minute,
// and when the newest was at most once an hour, as a credential's last use is (issue #996).
func (s *Service) CountArtifactShareView(ctx context.Context, id string) error {
	now := s.clock.Now()
	v := s.shareViews

	v.mu.Lock()
	p := v.pending[id]
	p.count++
	p.last = now.UnixMilli()
	v.pending[id] = p

	if now.Sub(v.written) < shareViewsWriteEvery {
		v.mu.Unlock()

		return nil
	}

	batch := v.pending
	v.pending, v.written = map[string]shareView{}, now
	v.mu.Unlock()

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		for linkID, b := range batch {
			if err := q.CountArtifactShareView(ctx, store.CountArtifactShareViewParams{
				Views: b.count, Now: b.last, Before: b.last - bearertoken.TouchEvery.Milliseconds(), ID: linkID,
			}); err != nil {
				return fmt.Errorf("counting the views of link %s: %w", linkID, err)
			}
		}

		return nil
	})
	if err != nil {
		v.add(batch)
	}

	return err
}

// withUnwrittenViews adds to a link its openings not written yet.
func (s *Service) withUnwrittenViews(sh ArtifactShare) ArtifactShare {
	p := s.shareViews.unwritten(sh.ID)
	if p.count == 0 {
		return sh
	}

	sh.Views += p.count
	if last := millis(p.last); sh.LastViewedAt == nil || last.After(*sh.LastViewedAt) {
		sh.LastViewedAt = &last
	}

	return sh
}
