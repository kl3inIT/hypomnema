package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// errNoMessage is a message the mirror does not keep.
var errNoMessage = errors.New("whatsapp: not kept")

// mediaRow is what downloading a kept message's media needs, and what was kept of it (issue #634).
type mediaRow struct {
	mediaType   string
	mime        string
	fileName    string
	directPath  string
	mediaKey    []byte
	fileSHA256  []byte
	fileEncHash []byte
	fileLength  int64
	localPath   string
	// sender and fromMe name the message to the phone when it is asked for the file again (issue #651).
	sender string
	fromMe bool
}

func (m *mirror) mediaOf(ctx context.Context, account, chat, id string) (mediaRow, error) {
	var r mediaRow

	err := m.db.QueryRowContext(ctx, `SELECT media_type, mime_type, filename, direct_path, media_key, file_sha256,
			file_enc_sha256, file_length, local_path, sender_jid, from_me
		FROM wa_messages WHERE account = ? AND chat_jid = ? AND msg_id = ?`, account, chat, id).
		Scan(&r.mediaType, &r.mime, &r.fileName, &r.directPath, &r.mediaKey, &r.fileSHA256, &r.fileEncHash,
			&r.fileLength, &r.localPath, &r.sender, &r.fromMe)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: no message %s is kept in %s", errNoMessage, id, chat)
	}

	return r, err
}

// pendingMedia lists the kept media messages with no file yet, the newest first (wacli media backfill).
func (m *mirror) pendingMedia(ctx context.Context, account string, limit int) ([]mediaJob, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT chat_jid, msg_id FROM wa_messages
		WHERE account = ? AND direct_path <> '' AND local_path = '' AND media_error = '' AND revoked = 0
		ORDER BY ts DESC LIMIT ?`, account, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	var out []mediaJob

	for rows.Next() {
		j := mediaJob{account: account}
		if err := rows.Scan(&j.chat, &j.id); err != nil {
			return nil, err
		}

		out = append(out, j)
	}

	return out, rows.Err()
}

// anchor is the oldest message kept in a chat, which a request for older ones starts from.
type anchor struct {
	id     string
	at     time.Time
	fromMe bool
}

func (m *mirror) oldest(ctx context.Context, account, chat string) (anchor, error) {
	var (
		a  anchor
		ts int64
	)

	err := m.db.QueryRowContext(ctx, `SELECT msg_id, ts, from_me FROM wa_messages
		WHERE account = ? AND chat_jid = ? ORDER BY ts, rowid LIMIT 1`, account, chat).Scan(&a.id, &ts, &a.fromMe)
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: no message of %s is kept yet, to ask for older ones from", errNoMessage, chat)
	}

	a.at = time.Unix(ts, 0)

	return a, err
}

// quotedRow is what a reply quotes of the message it answers.
type quotedRow struct {
	sender, text string
	fromMe       bool
}

func (m *mirror) quoted(ctx context.Context, account, chat, id string) (quotedRow, error) {
	var q quotedRow

	err := m.db.QueryRowContext(ctx, `SELECT sender_jid, CASE WHEN text <> '' THEN text ELSE media_caption END, from_me
		FROM wa_messages WHERE account = ? AND chat_jid = ? AND msg_id = ?`, account, chat, id).
		Scan(&q.sender, &q.text, &q.fromMe)
	if errors.Is(err, sql.ErrNoRows) {
		return q, fmt.Errorf("%w: no message %s is kept in %s to reply to", errNoMessage, id, chat)
	}

	return q, err
}

// count is how many messages of a chat are kept.
func (m *mirror) count(ctx context.Context, account, chat string) (int, error) {
	var n int

	err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM wa_messages WHERE account = ? AND chat_jid = ?`, account,
		chat).Scan(&n)

	return n, err
}

// movedMedia records the new path of a file the phone uploaded again.
func (m *mirror) movedMedia(ctx context.Context, account, chat, id, path string) error {
	_, err := m.db.ExecContext(ctx, `UPDATE wa_messages SET direct_path = ? WHERE account = ? AND chat_jid = ?
		AND msg_id = ?`, path, account, chat, id)

	return err
}

// keptMedia records where a message's file was kept, or why it was not.
func (m *mirror) keptMedia(ctx context.Context, account, chat, id, path, why string) error {
	at := int64(0)
	if path != "" {
		at = time.Now().Unix()
	}

	_, err := m.db.ExecContext(ctx, `UPDATE wa_messages SET local_path = ?, downloaded_at = ?, media_error = ?
		WHERE account = ? AND chat_jid = ? AND msg_id = ?`, path, at, why, account, chat, id)

	return err
}
