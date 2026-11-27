package inboxstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// A Bot's result reaches the item (issue #1207): a Bot's comment carries the files it made, as a reply's MEDIA:
// lines name them (paths in the Bot's folder, issue #699), and the Artifacts it showed, as Gitea's comment carries
// its attachments (models/issues/comment.go: Comment.Attachments, UpdateCommentAttachments), which the Inbox draws
// under the comment's text as the chat draws a reply's files and an Artifact's card. The comment's event keeps them
// beside its text: after.files, after.artifacts, and after.chat, the chat whose Bot's folder the files are read
// from, as the chat reads a reply's files from its own Bot's folder.

// MaxCommentFiles and MaxCommentArtifacts bound what one comment carries, Gitea's default MAX_FILES for one
// upload's attachments (modules/setting/attachment.go).
const (
	MaxCommentFiles     = 5
	MaxCommentArtifacts = 5
)

// maxFilePath bounds a file's path, as a path in the Bot's folder is read (workspacefs).
const maxFilePath = 1024

// Attached is what a Bot's comment carries besides its text.
type Attached struct {
	// Files are paths in the Bot's folder, relative to it, as a reply's MEDIA: lines name them.
	Files []string
	// Artifacts are ids of Artifacts the Bot showed.
	Artifacts []string
	// From is the Bot's chat it comments from: its files are read through the item's own chat when that is the
	// Bot's, else through this one.
	From string
}

// checked is what a comment carries, its paths made relative as the chat reads them (media.ts folderPath), or why
// it cannot carry it: only a Bot's comment carries anything, a path stays in its folder, and an Artifact is named
// once.
func (a Attached) checked(actor Actor) (Attached, error) {
	if len(a.Files) == 0 && len(a.Artifacts) == 0 {
		return Attached{}, nil
	}

	if actor.Kind != Bot {
		return Attached{}, fmt.Errorf("%w: only a Bot's comment carries files and Artifacts", storeerr.ErrInvalid)
	}

	if len(a.Files) > MaxCommentFiles || len(a.Artifacts) > MaxCommentArtifacts {
		return Attached{}, fmt.Errorf("%w: a comment carries at most %d files and %d Artifacts", storeerr.ErrInvalid,
			MaxCommentFiles, MaxCommentArtifacts)
	}

	out := Attached{From: a.From}

	for _, f := range a.Files {
		p, err := folderPath(f)
		if err != nil {
			return Attached{}, err
		}

		out.Files = append(out.Files, p)
	}

	for _, id := range a.Artifacts {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out.Artifacts, id) {
			return Attached{}, fmt.Errorf("%w: an Artifact is named once, by its id art_…", storeerr.ErrInvalid)
		}

		out.Artifacts = append(out.Artifacts, id)
	}

	return out, nil
}

// folderPath is a file's path relative to the Bot's folder, without the ./ a reply may begin it with.
func folderPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}

	clean := path.Clean(p)

	switch {
	case p == "" || utf8.RuneCountInString(p) > maxFilePath:
		return "", fmt.Errorf("%w: a file is a path in your folder, 1 to %d characters", storeerr.ErrInvalid,
			maxFilePath)
	case path.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, "../"):
		return "", fmt.Errorf("%w: file %q is outside your folder; name it relative to the folder, as a MEDIA: "+
			"line does", storeerr.ErrInvalid, raw)
	}

	return clean, nil
}

// after is the comment's event: its text and what it carries. Each Artifact is one the commenting Bot showed, as
// Gitea links only an attachment of the issue's own repository (models/issues/issue_update.go,
// validateAttachmentForIssue); the files are read through the item's own chat when it is the Bot's, else through
// the chat the Bot comments from.
func (a Attached) after(ctx context.Context, q *store.Queries, actor Actor, text string, cur store.Todo,
	chatBot string,
) (map[string]any, error) {
	after := map[string]any{"text": text}

	for _, id := range a.Artifacts {
		art, err := q.GetArtifact(ctx, id)

		switch {
		case errors.Is(err, sql.ErrNoRows) || err == nil && art.BotID != actor.ID:
			return nil, fmt.Errorf("%w: Artifact %s is not one you showed", storeerr.ErrForbidden, id)
		case err != nil:
			return nil, fmt.Errorf("reading artifact %s: %w", id, err)
		}
	}

	if len(a.Artifacts) > 0 {
		after["artifacts"] = a.Artifacts
	}

	if len(a.Files) == 0 {
		return after, nil
	}

	chat := a.From
	if cur.ChatID.Valid && chatBot == actor.ID {
		chat = cur.ChatID.String
	}

	if chat == "" {
		return nil, fmt.Errorf("%w: files are read from your folder through one of your chats; this call has none",
			storeerr.ErrInvalid)
	}

	after["files"], after["chat"] = a.Files, chat

	return after, nil
}

// liveChats points each Bot's comment whose files are read through a chat deleted since at a chat of the same Bot
// that is still there (issue #1207, review): every chat of a Bot reads its one folder, so the files it named are
// still read. The event keeps the chat it named; only what is read changes. A Bot with no chat left keeps it, and
// its chips say the server's refusal.
func liveChats(ctx context.Context, q *store.Queries, events []Event) error {
	live := map[string]bool{}
	instead := map[string]string{}

	for i, e := range events {
		if e.Kind != EventCommented || e.Actor.Kind != Bot {
			continue
		}

		var after map[string]json.RawMessage
		if json.Unmarshal(e.After, &after) != nil || after["chat"] == nil {
			continue
		}

		var chat string
		if json.Unmarshal(after["chat"], &chat) != nil || chat == "" {
			continue
		}

		there, seen := live[chat]
		if !seen {
			_, err := q.GetTodoChatBot(ctx, chat)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("reading chat %s: %w", chat, err)
			}

			there = err == nil
			live[chat] = there
		}

		if there {
			continue
		}

		other, seen := instead[e.Actor.ID]
		if !seen {
			var err error
			if other, err = q.GetBotLiveChat(ctx, e.Actor.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("reading a chat of Bot %s: %w", e.Actor.ID, err)
			}

			instead[e.Actor.ID] = other
		}

		if other == "" {
			continue
		}

		after["chat"], _ = json.Marshal(other)

		raw, err := json.Marshal(after)
		if err != nil {
			return fmt.Errorf("reading comment %d: %w", e.ID, err)
		}

		events[i].After = raw
	}

	return nil
}
