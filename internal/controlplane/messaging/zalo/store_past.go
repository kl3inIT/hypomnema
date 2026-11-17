package zalo

// What step 2 of #613 keeps (issue #669): where each group's walk back through its past is, where the direct
// chats' queue is, and each message's file, as WhatsApp's mirror keeps its files (whatsapp/store_media.go).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// mimeOf is a file's type, from its name, or from its kind when its name says nothing (Zalo's images and voice
// notes have none).
func mimeOf(mediaType, fileName string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName))); t != "" {
		return t
	}

	switch mediaType {
	case "image":
		return "image/jpeg"
	case "video":
		return "video/mp4"
	case "audio":
		return "audio/mp4"
	}

	return ""
}

// walk is where a group's walk back through its past is (zca-js getCloudMessage's lastMsgId and isOld).
type walk struct {
	chat   string
	cursor string
	old    bool
	done   bool
}

// groupsToWalk are the groups whose past is not walked to its start yet, the latest first.
func (m *mirror) groupsToWalk(ctx context.Context, account string) ([]walk, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id, history_cursor, history_old FROM zl_chats
		WHERE account = ? AND kind = 'group' AND history_done = 0 ORDER BY last_message_ts DESC, id`, account)
	if err != nil {
		return nil, fmt.Errorf("zalo: listing the groups to walk: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	var out []walk

	for rows.Next() {
		var w walk
		if err := rows.Scan(&w.chat, &w.cursor, &w.old); err != nil {
			return nil, err
		}

		out = append(out, w)
	}

	return out, rows.Err()
}

// walkOf is where one chat's walk is.
func (m *mirror) walkOf(ctx context.Context, account, chat string) (walk, error) {
	w := walk{chat: chat}

	err := m.db.QueryRowContext(ctx, `SELECT history_cursor, history_old, history_done FROM zl_chats
		WHERE account = ? AND id = ?`, account, chat).Scan(&w.cursor, &w.old, &w.done)
	if errors.Is(err, sql.ErrNoRows) {
		return w, nil
	}

	return w, err
}

// walked keeps where a group's walk got to.
func (m *mirror) walked(ctx context.Context, account string, w walk) error {
	_, err := m.db.ExecContext(ctx, `UPDATE zl_chats SET history_cursor = ?, history_old = ?, history_done = ?
		WHERE account = ? AND id = ?`, w.cursor, w.old, w.done, account, w.chat)
	if err != nil {
		return fmt.Errorf("zalo: keeping where the past of %s is: %w", w.chat, err)
	}

	return nil
}

// queue is where the walk of the direct chats' queue is.
func (m *mirror) queue(ctx context.Context, account string) (cursor string, done bool, err error) {
	err = m.db.QueryRowContext(ctx, `SELECT cursor, done FROM zl_queue WHERE account = ?`, account).
		Scan(&cursor, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	return cursor, done, err
}

func (m *mirror) queued(ctx context.Context, account, cursor string, done bool) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO zl_queue (account, cursor, done) VALUES (?, ?, ?)
		ON CONFLICT (account) DO UPDATE SET cursor = excluded.cursor, done = excluded.done`, account, cursor, done)
	if err != nil {
		return fmt.Errorf("zalo: keeping where the direct chats' past is: %w", err)
	}

	return nil
}

// unnamed are the direct chats with no name yet.
func (m *mirror) unnamed(ctx context.Context, account string) ([]string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM zl_chats WHERE account = ? AND kind = 'dm' AND name = ''`,
		account)
	if err != nil {
		return nil, fmt.Errorf("zalo: listing the direct chats with no name: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	var out []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}

		out = append(out, id)
	}

	return out, rows.Err()
}

// mediaRow is what downloading a message's file needs.
type mediaRow struct {
	mediaType, url, fileName, mime, localPath string
	size                                      int64
}

func (m *mirror) mediaOf(ctx context.Context, account, chat, id string) (mediaRow, error) {
	var r mediaRow

	err := m.db.QueryRowContext(ctx, `SELECT media_type, attachment_url, attachment_title, mime_type, local_path,
		file_size FROM zl_messages WHERE account = ? AND chat_id = ? AND msg_id = ?`, account, chat, id).
		Scan(&r.mediaType, &r.url, &r.fileName, &r.mime, &r.localPath, &r.size)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: message %s of chat %s", errNoMessage, id, chat)
	}

	return r, err
}

// keptMedia records a message's file kept at path, or why it is not kept, which no download tries again.
func (m *mirror) keptMedia(ctx context.Context, account, chat, id, path, why, mimeType string) error {
	_, err := m.db.ExecContext(ctx, `UPDATE zl_messages SET local_path = ?, media_error = ?,
		mime_type = CASE WHEN ? <> '' THEN ? ELSE mime_type END
		WHERE account = ? AND chat_id = ? AND msg_id = ?`, path, why, mimeType, mimeType, account, chat, id)
	if err != nil {
		return fmt.Errorf("zalo: keeping the file of message %s: %w", id, err)
	}

	return nil
}

// pendingMedia are kept messages whose file is not downloaded yet nor given up on, the newest first, older than
// before (in milliseconds) unless it is 0.
func (m *mirror) pendingMedia(ctx context.Context, account string, before int64, limit int) ([]mediaJob, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT chat_id, msg_id, ts FROM zl_messages
		WHERE account = ? AND attachment_url <> '' AND local_path = '' AND media_error = ''
			AND media_type IN ('image', 'video', 'audio', 'document') AND (? = 0 OR ts < ?)
		ORDER BY ts DESC LIMIT ?`, account, before, before, limit)
	if err != nil {
		return nil, fmt.Errorf("zalo: listing the files to download: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	var out []mediaJob

	for rows.Next() {
		j := mediaJob{account: account}
		if err := rows.Scan(&j.chat, &j.id, &j.ts); err != nil {
			return nil, err
		}

		out = append(out, j)
	}

	return out, rows.Err()
}

// chatMessages counts a chat's kept messages.
func (m *mirror) chatMessages(ctx context.Context, account, chat string) (int, error) {
	var n int

	err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM zl_messages WHERE account = ? AND chat_id = ?`, account,
		chat).Scan(&n)

	return n, err
}

// chatKind is a kept chat's kind, dm or group; empty when it is not kept.
func (m *mirror) chatKind(ctx context.Context, account, id string) string {
	var kind string
	if m.db.QueryRowContext(ctx, `SELECT kind FROM zl_chats WHERE account = ? AND id = ?`, account, id).
		Scan(&kind) != nil {
		return ""
	}

	return kind
}

// quoteOf is a kept message as a reply quotes it.
func (m *mirror) quoteOf(ctx context.Context, account, chat, id string) (quote, error) {
	q := quote{ID: id}

	err := m.db.QueryRowContext(ctx, `SELECT cli_msg_id, sender_id, ts, kind, text FROM zl_messages
		WHERE account = ? AND chat_id = ? AND msg_id = ?`, account, chat, id).
		Scan(&q.CliID, &q.From, &q.At, &q.Kind, &q.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return q, fmt.Errorf("%w: no message %s is kept in chat %s", orchestration.ErrNotFound, id, chat)
	}

	return q, err
}
