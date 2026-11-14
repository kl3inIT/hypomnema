package orchestration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Artifacts (issue #402): what a Bot hands the owner to look at in its chat, after OpenMausBot's chat pages
// (server/chat-apps.ts:160-207, server/app-docs.ts): a Bot shows one with `hypomnema artifact show`, the chat gets
// one message of kind artifact for it, and showing it again by its id makes its next revision and updates that
// message in place. Each revision keeps its files, and the files are kept once by their SHA-256. The last ten
// revisions are kept (OpenMausBot's APP_PAGE_VERSIONS).

// ArtifactIDPrefix starts every artifact's id.
const ArtifactIDPrefix = "art"

// ArtifactRevisionsKept is how many revisions of an artifact are kept, the newest included.
const ArtifactRevisionsKept = 10

// Artifact is an artifact at its newest revision. Revisions are every revision kept, newest first, when it was
// read alone.
type Artifact struct {
	ID     string
	ChatID string
	Bot    string
	// MessageSeq is its message in the chat.
	MessageSeq int64
	Revision   int
	Title      string
	Type       artifactpack.Type
	Entry      string
	Size       int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Revisions  []ArtifactRevision
}

// ArtifactRevision is one revision of an artifact, with its files.
type ArtifactRevision struct {
	Revision  int
	Title     string
	Type      artifactpack.Type
	Entry     string
	Size      int64
	Files     []ArtifactFileInfo
	CreatedAt time.Time
}

// ArtifactFileInfo is a file of a revision, without its bytes.
type ArtifactFileInfo struct {
	Path      string
	Size      int64
	MediaType string
}

// ArtifactFile is a file of a revision with its bytes.
type ArtifactFile struct {
	ArtifactFileInfo
	Data []byte
}

// ArtifactCard is what an artifact's message shows: the artifact as it is now.
type ArtifactCard struct {
	ID        string
	Revision  int
	Title     string
	Type      artifactpack.Type
	UpdatedAt time.Time
}

// ArtifactMaker is who shows an artifact: a Bot in its chat (a pi's token), or a Bot's Routine's script, which
// has no chat and only replaces an artifact of its Bot (issue #402, row 11).
type ArtifactMaker struct {
	Bot  string
	Chat string
}

// ShowArtifact is an artifact to show: a new one, or the next revision of ID.
type ShowArtifact struct {
	// ID names the artifact to replace; empty for a new one.
	ID string
	// Title is its title; a replacement without one keeps the title it had.
	Title string
	Pack  artifactpack.Pack
}

// ShowArtifact keeps an artifact a Bot shows: a new one with its message in the Bot's chat, or the next revision
// of one it showed, which updates that message. The same files under the same title make no new revision, and it
// says whether it kept one.
func (s *Service) ShowArtifact(ctx context.Context, by ArtifactMaker, in ShowArtifact) (Artifact, bool, error) {
	title := strings.TrimSpace(in.Title)
	if title != "" || in.ID == "" {
		if err := artifactpack.CheckTitle(title); err != nil {
			return Artifact{}, false, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}

	if by.Bot == "" {
		return Artifact{}, false, fmt.Errorf("%w: only a Bot shows an artifact", ErrForbidden)
	}

	id, changed := in.ID, false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if in.ID == "" {
			made, err := s.newArtifact(ctx, q, by, title, in.Pack, now)
			id, changed = made, err == nil

			return err
		}

		var err error

		changed, err = reviseArtifact(ctx, q, by, in.ID, title, in.Pack, now)

		return err
	})
	if err != nil {
		return Artifact{}, false, fmt.Errorf("showing an artifact: %w", err)
	}

	a, err := s.GetArtifact(ctx, id)

	return a, changed, err
}

func (s *Service) newArtifact(ctx context.Context, q *store.Queries, by ArtifactMaker, title string,
	p artifactpack.Pack, now int64,
) (string, error) {
	if by.Chat == "" {
		return "", fmt.Errorf("%w: a Routine's run makes no artifact; it replaces one of its Bot's with --id",
			ErrForbidden)
	}

	chat, err := q.GetChat(ctx, by.Chat)
	if err != nil {
		return "", notFound(err, "chat", by.Chat)
	}

	if chat.Bot != by.Bot {
		return "", fmt.Errorf("%w: chat %s is %s's, not %s's", ErrForbidden, by.Chat, chat.Bot, by.Bot)
	}

	seq, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{
		ChatID: by.Chat, Role: string(ChatSystem), Kind: string(ChatArtifact), Text: title, Now: now,
	})
	if err != nil {
		return "", fmt.Errorf("adding the artifact's message: %w", err)
	}

	id := s.newID(ArtifactIDPrefix)

	if err := q.InsertArtifact(ctx, store.InsertArtifactParams{
		ID: id, ChatID: by.Chat, Bot: by.Bot, MessageSeq: seq, Revision: 1, Now: now,
	}); err != nil {
		return "", fmt.Errorf("keeping the artifact: %w", err)
	}

	if err := keepRevision(ctx, q, id, 1, title, p, now); err != nil {
		return "", err
	}

	return id, q.TouchChat(ctx, store.TouchChatParams{ID: by.Chat, Now: now})
}

func reviseArtifact(ctx context.Context, q *store.Queries, by ArtifactMaker, id, title string,
	p artifactpack.Pack, now int64,
) (bool, error) {
	a, err := q.GetArtifact(ctx, id)
	// One of another Bot, or of another chat than the pi's, is not there for it (OpenMausBot: "No page with that
	// id in this conversation").
	if errors.Is(err, sql.ErrNoRows) || err == nil && (a.Bot != by.Bot || by.Chat != "" && a.ChatID != by.Chat) {
		return false, fmt.Errorf("%w: no artifact %s of this chat; show a new one without --id", ErrNotFound, id)
	}

	if err != nil {
		return false, fmt.Errorf("reading artifact %s: %w", id, err)
	}

	revs, err := q.ListArtifactRevisions(ctx, id)
	if err != nil || len(revs) == 0 {
		return false, errors.Join(err, fmt.Errorf("reading the revisions of artifact %s", id))
	}

	latest := revs[0]
	if title == "" {
		title = latest.Title
	}

	if latest.Digest == p.Digest && latest.Title == title {
		return false, nil
	}

	next := a.Revision + 1
	if err := keepRevision(ctx, q, id, next, title, p, now); err != nil {
		return false, err
	}

	if err := q.ReviseArtifact(ctx, store.ReviseArtifactParams{ID: id, Revision: next, Now: now}); err != nil {
		return false, fmt.Errorf("revising artifact %s: %w", id, err)
	}

	if err := q.SetArtifactMessage(ctx, store.SetArtifactMessageParams{Seq: a.MessageSeq, Text: title}); err != nil {
		return false, fmt.Errorf("updating the message of artifact %s: %w", id, err)
	}

	if err := pruneRevisions(ctx, q, id, next-ArtifactRevisionsKept+1); err != nil {
		return false, err
	}

	return true, q.TouchChat(ctx, store.TouchChatParams{ID: a.ChatID, Now: now})
}

// keepRevision keeps a revision's files, their bytes once by SHA-256.
func keepRevision(ctx context.Context, q *store.Queries, id string, rev int64, title string, p artifactpack.Pack,
	now int64,
) error {
	if err := q.InsertArtifactRevision(ctx, store.InsertArtifactRevisionParams{
		ArtifactID: id, Revision: rev, Title: title, Type: string(p.Type), Entry: p.Entry, Digest: p.Digest,
		Size: p.Size, Now: now,
	}); err != nil {
		return fmt.Errorf("keeping revision %d of artifact %s: %w", rev, id, err)
	}

	for _, f := range p.Files {
		sum := sha256.Sum256(f.Data)
		hash := hex.EncodeToString(sum[:])

		if err := q.InsertArtifactBlob(ctx, store.InsertArtifactBlobParams{
			Sha256: hash, Size: int64(len(f.Data)), Data: f.Data,
		}); err != nil {
			return fmt.Errorf("keeping %s: %w", f.Path, err)
		}

		if err := q.InsertArtifactFile(ctx, store.InsertArtifactFileParams{
			ArtifactID: id, Revision: rev, Path: f.Path, Sha256: hash, Size: int64(len(f.Data)),
			MediaType: artifactpack.MediaType(p, f),
		}); err != nil {
			return fmt.Errorf("keeping %s: %w", f.Path, err)
		}
	}

	return nil
}

// pruneRevisions takes away the revisions before keep, and the bytes no other revision names.
func pruneRevisions(ctx context.Context, q *store.Queries, id string, keep int64) error {
	if keep <= 1 {
		return nil
	}

	blobs, err := q.ListArtifactBlobsBefore(ctx, store.ListArtifactBlobsBeforeParams{ArtifactID: id, Revision: keep})
	if err != nil {
		return fmt.Errorf("reading the old revisions of artifact %s: %w", id, err)
	}

	if err := q.DeleteArtifactRevisionsBefore(ctx, store.DeleteArtifactRevisionsBeforeParams{
		ArtifactID: id, Revision: keep,
	}); err != nil {
		return fmt.Errorf("removing the old revisions of artifact %s: %w", id, err)
	}

	if len(blobs) == 0 {
		return nil
	}

	candidates, err := json.Marshal(blobs)
	if err != nil {
		return fmt.Errorf("listing old files: %w", err)
	}

	if err := q.DeleteUnusedArtifactBlobs(ctx, string(candidates)); err != nil {
		return fmt.Errorf("removing old files of artifact %s: %w", id, err)
	}

	return nil
}

// GetArtifact returns an artifact with every revision kept, newest first, and each revision's files.
func (s *Service) GetArtifact(ctx context.Context, id string) (Artifact, error) {
	q := s.read()

	a, err := q.GetArtifact(ctx, id)
	if err != nil {
		return Artifact{}, notFound(err, "artifact", id)
	}

	revs, err := q.ListArtifactRevisions(ctx, id)
	if err != nil || len(revs) == 0 {
		return Artifact{}, errors.Join(err, fmt.Errorf("reading the revisions of artifact %s", id))
	}

	out := Artifact{
		ID: a.ID, ChatID: a.ChatID, Bot: a.Bot, MessageSeq: a.MessageSeq, Revision: int(a.Revision),
		Title: revs[0].Title, Type: artifactpack.Type(revs[0].Type), Entry: revs[0].Entry, Size: revs[0].Size,
		CreatedAt: millis(a.CreatedAt), UpdatedAt: millis(a.UpdatedAt),
	}

	for _, r := range revs {
		files, err := q.ListArtifactFiles(ctx, store.ListArtifactFilesParams{ArtifactID: id, Revision: r.Revision})
		if err != nil {
			return Artifact{}, fmt.Errorf("reading the files of artifact %s: %w", id, err)
		}

		rev := ArtifactRevision{
			Revision: int(r.Revision), Title: r.Title, Type: artifactpack.Type(r.Type), Entry: r.Entry,
			Size: r.Size, CreatedAt: millis(r.CreatedAt), Files: make([]ArtifactFileInfo, 0, len(files)),
		}

		for _, f := range files {
			rev.Files = append(rev.Files, ArtifactFileInfo{Path: f.Path, Size: f.Size, MediaType: f.MediaType})
		}

		out.Revisions = append(out.Revisions, rev)
	}

	return out, nil
}

// ArtifactFilter narrows a list of artifacts.
type ArtifactFilter struct {
	ChatID string
	Bot    string
	Limit  int
}

// ListArtifacts returns artifacts at their newest revision, the latest changed first.
func (s *Service) ListArtifacts(ctx context.Context, f ArtifactFilter) ([]Artifact, error) {
	rows, err := s.read().ListArtifacts(ctx, store.ListArtifactsParams{
		ChatID: f.ChatID, Bot: f.Bot, Lim: int64(pageLimit(f.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}

	out := make([]Artifact, 0, len(rows))
	for _, r := range rows {
		out = append(out, Artifact{
			ID: r.ID, ChatID: r.ChatID, Bot: r.Bot, MessageSeq: r.MessageSeq, Revision: int(r.Revision),
			Title: r.Title, Type: artifactpack.Type(r.Type), Entry: r.Entry, Size: r.Size,
			CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
		})
	}

	return out, nil
}

// ReadArtifactFile returns a file of a revision with its bytes: the one shown when path is empty.
func (s *Service) ReadArtifactFile(ctx context.Context, id string, rev int, path string) (ArtifactFile, error) {
	q := s.read()

	if path == "" {
		a, err := s.GetArtifact(ctx, id)
		if err != nil {
			return ArtifactFile{}, err
		}

		r, ok := revisionOf(a, rev)
		if !ok {
			return ArtifactFile{}, fmt.Errorf("%w: revision %d of artifact %s", ErrNotFound, rev, id)
		}

		path = r.Entry
	}

	f, err := q.ArtifactFileData(ctx, store.ArtifactFileDataParams{ArtifactID: id, Revision: int64(rev), Path: path})
	if err != nil {
		return ArtifactFile{}, notFound(err, "file of artifact "+id, path)
	}

	return ArtifactFile{ArtifactFileInfo: ArtifactFileInfo{Path: f.Path, Size: f.Size, MediaType: f.MediaType},
		Data: f.Data}, nil
}

// ReadArtifactRevision returns every file of a revision with its bytes.
func (s *Service) ReadArtifactRevision(ctx context.Context, id string, rev int) ([]ArtifactFile, error) {
	rows, err := s.read().ArtifactRevisionData(ctx, store.ArtifactRevisionDataParams{
		ArtifactID: id, Revision: int64(rev),
	})
	if err != nil {
		return nil, fmt.Errorf("reading revision %d of artifact %s: %w", rev, id, err)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: revision %d of artifact %s", ErrNotFound, rev, id)
	}

	out := make([]ArtifactFile, 0, len(rows))
	for _, r := range rows {
		out = append(out, ArtifactFile{
			ArtifactFileInfo: ArtifactFileInfo{Path: r.Path, Size: r.Size, MediaType: r.MediaType}, Data: r.Data,
		})
	}

	return out, nil
}

func revisionOf(a Artifact, rev int) (ArtifactRevision, bool) {
	for _, r := range a.Revisions {
		if r.Revision == rev {
			return r, true
		}
	}

	return ArtifactRevision{}, false
}

// ListChatArtifactMessages returns a chat's artifacts' messages, each with its artifact as it is now: a chat's
// stream sends one again when its artifact was replaced.
func (s *Service) ListChatArtifactMessages(ctx context.Context, chatID string) ([]ChatMessage, error) {
	rows, err := s.read().ListChatArtifactMessages(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("listing the artifacts of chat %s: %w", chatID, err)
	}

	out := make([]ChatMessage, 0, len(rows))
	for _, r := range rows {
		m := chatMessage(r.ChatMessage)
		m.Artifact = &ArtifactCard{
			ID: r.ArtifactID, Revision: int(r.ArtifactRevision), Title: r.ArtifactTitle,
			Type: artifactpack.Type(r.ArtifactType), UpdatedAt: millis(r.ArtifactUpdatedAt),
		}
		out = append(out, m)
	}

	return out, nil
}

// ArtifactRef names an artifact and its title.
type ArtifactRef struct {
	ID    string
	Title string
	Type  artifactpack.Type
}

// ArtifactsBeforeReply returns the artifacts made or replaced in a chat since its last reply with text before
// the reply at seq: what that reply follows, which the Lark relay names under it (issue #402, row 12).
func (s *Service) ArtifactsBeforeReply(ctx context.Context, chatID string, seq int64) ([]ArtifactRef, error) {
	rows, err := s.read().ArtifactsBeforeReply(ctx, store.ArtifactsBeforeReplyParams{ChatID: chatID, ReplySeq: seq})
	if err != nil {
		return nil, fmt.Errorf("reading the artifacts of chat %s: %w", chatID, err)
	}

	out := make([]ArtifactRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, ArtifactRef{ID: r.ID, Title: r.Title, Type: artifactpack.Type(r.Type)})
	}

	return out, nil
}
