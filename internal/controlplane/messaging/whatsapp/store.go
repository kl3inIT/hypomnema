package whatsapp

// The mirror of every chat (issue #631): its own tables in whatsapp.db, beside whatsmeow's, keyed by the account
// (the number) so that several Connections share the file and a number linked again keeps its history. The shape
// is wacli's (internal/store/schema.sql: chats, groups, messages; migrations.go: messages_fts with its triggers),
// cut to what is kept here; every table is prefixed wa_ so that none is mistaken for whatsmeow's.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	defaultLimit = 50
	maxLimit     = 500
)

// schema is the mirror's tables as they were first made (issue #631); upgrades change them, in order.
const schema = `
CREATE TABLE IF NOT EXISTS wa_chats (
	account TEXT NOT NULL,
	jid TEXT NOT NULL,
	kind TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	last_message_ts INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, jid)
);
CREATE INDEX IF NOT EXISTS wa_chats_last ON wa_chats (account, last_message_ts DESC);

CREATE TABLE IF NOT EXISTS wa_messages (
	rowid INTEGER PRIMARY KEY AUTOINCREMENT,
	account TEXT NOT NULL,
	chat_jid TEXT NOT NULL,
	chat_name TEXT NOT NULL DEFAULT '',
	msg_id TEXT NOT NULL,
	sender_jid TEXT NOT NULL DEFAULT '',
	sender_name TEXT NOT NULL DEFAULT '',
	ts INTEGER NOT NULL,
	from_me INTEGER NOT NULL,
	text TEXT NOT NULL DEFAULT '',
	quoted_msg_id TEXT NOT NULL DEFAULT '',
	media_type TEXT NOT NULL DEFAULT '',
	media_caption TEXT NOT NULL DEFAULT '',
	filename TEXT NOT NULL DEFAULT '',
	mime_type TEXT NOT NULL DEFAULT '',
	edited INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0,
	UNIQUE (account, chat_jid, msg_id)
);
CREATE INDEX IF NOT EXISTS wa_messages_chat_ts ON wa_messages (account, chat_jid, ts DESC);
CREATE INDEX IF NOT EXISTS wa_messages_ts ON wa_messages (account, ts DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS wa_messages_fts USING fts5(text, media_caption, filename, chat_name, sender_name);

CREATE TRIGGER IF NOT EXISTS wa_messages_ai AFTER INSERT ON wa_messages BEGIN
	INSERT INTO wa_messages_fts(rowid, text, media_caption, filename, chat_name, sender_name)
	VALUES (new.rowid, new.text, new.media_caption, new.filename, new.chat_name, new.sender_name);
END;
CREATE TRIGGER IF NOT EXISTS wa_messages_ad AFTER DELETE ON wa_messages BEGIN
	DELETE FROM wa_messages_fts WHERE rowid = old.rowid;
END;
CREATE TRIGGER IF NOT EXISTS wa_messages_au AFTER UPDATE ON wa_messages BEGIN
	DELETE FROM wa_messages_fts WHERE rowid = old.rowid;
	INSERT INTO wa_messages_fts(rowid, text, media_caption, filename, chat_name, sender_name)
	VALUES (new.rowid, new.text, new.media_caption, new.filename, new.chat_name, new.sender_name);
END;

CREATE TABLE IF NOT EXISTS wa_groups (
	account TEXT NOT NULL,
	jid TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (account, jid)
);
`

// upgrades bring the mirror's tables to this version, each from the one before; wa_version says which ran.
var upgrades = []string{
	// 1: a media message keeps what downloading it needs, and where and when its file was kept (issue #634), as
	// wacli's messages do (direct_path, media_key, file_sha256, file_enc_sha256, file_length, local_path,
	// downloaded_at); media_error says why it was not.
	`ALTER TABLE wa_messages ADD COLUMN direct_path TEXT NOT NULL DEFAULT '';
	ALTER TABLE wa_messages ADD COLUMN media_key BLOB;
	ALTER TABLE wa_messages ADD COLUMN file_sha256 BLOB;
	ALTER TABLE wa_messages ADD COLUMN file_enc_sha256 BLOB;
	ALTER TABLE wa_messages ADD COLUMN file_length INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE wa_messages ADD COLUMN local_path TEXT NOT NULL DEFAULT '';
	ALTER TABLE wa_messages ADD COLUMN downloaded_at INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE wa_messages ADD COLUMN media_error TEXT NOT NULL DEFAULT '';
	CREATE INDEX IF NOT EXISTS wa_messages_media_pending ON wa_messages (account, ts DESC)
		WHERE direct_path <> '' AND local_path = '' AND media_error = '';`,
	// 2: a file WhatsApp's servers no longer held was given up (#634); it is asked of the phone now (issue #651).
	`UPDATE wa_messages SET media_error = '' WHERE media_error = 'WhatsApp no longer has it';`,
}

// mirror reads and writes the kept chats and messages.
type mirror struct{ db *sql.DB }

func openMirror(ctx context.Context, db *sql.DB) (*mirror, error) {
	const versionTable = `CREATE TABLE IF NOT EXISTS wa_version (version INTEGER NOT NULL);`
	if _, err := db.ExecContext(ctx, schema+versionTable); err != nil {
		return nil, fmt.Errorf("whatsapp: preparing the mirror's tables: %w", err)
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM wa_version`).Scan(&version); err != nil {
		return nil, fmt.Errorf("whatsapp: reading the mirror's version: %w", err)
	}

	for v := version; v < len(upgrades); v++ {
		if err := upgrade(ctx, db, v+1, upgrades[v]); err != nil {
			return nil, err
		}
	}

	return &mirror{db: db}, nil
}

func upgrade(ctx context.Context, db *sql.DB, version int, stmt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.

	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("whatsapp: upgrading the mirror to version %d: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM wa_version; INSERT INTO wa_version (version) VALUES (?)`,
		version); err != nil {
		return err
	}

	return tx.Commit()
}

// kept is a message ready to keep: parsed, with its names and ids resolved.
type kept struct {
	parsed

	account  string
	chatKind string
	chatName string
}

// keep stores a message, or applies the edit or revoke it is, and moves its chat's last message on. A message
// kept twice (a history sync repeating a live one) is kept once.
func (m *mirror) keep(ctx context.Context, k kept) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.

	chat, ts := k.chat.String(), k.at.Unix()

	switch {
	case k.revoked:
		_, err = tx.ExecContext(ctx, `UPDATE wa_messages SET revoked = 1, text = '', media_caption = ''
			WHERE account = ? AND chat_jid = ? AND msg_id = ?`, k.account, chat, k.id)
	case k.edited:
		_, err = tx.ExecContext(ctx, `UPDATE wa_messages SET edited = 1, text = ?, media_caption = ?
			WHERE account = ? AND chat_jid = ? AND msg_id = ?`, k.text, k.caption, k.account, chat, k.id)
	default:
		_, err = tx.ExecContext(ctx, `INSERT INTO wa_messages (account, chat_jid, chat_name, msg_id, sender_jid,
			sender_name, ts, from_me, text, quoted_msg_id, media_type, media_caption, filename, mime_type,
			direct_path, media_key, file_sha256, file_enc_sha256, file_length)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account, chat_jid, msg_id) DO NOTHING`,
			k.account, chat, k.chatName, k.id, k.sender, k.pushName, ts, k.fromMe, k.text, k.quotedID, k.mediaType,
			k.caption, k.fileName, k.mime, k.directPath, k.mediaKey, k.fileSHA256, k.fileEncHash,
			min(k.fileLength, math.MaxInt64))
	}

	if err != nil {
		return fmt.Errorf("whatsapp: keeping message %s: %w", k.id, err)
	}

	if !k.revoked && !k.edited {
		if err := upsertChat(ctx, tx, k.account, chat, k.chatKind, k.chatName, ts); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func upsertChat(ctx context.Context, tx *sql.Tx, account, jid, kind, name string, ts int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO wa_chats (account, jid, kind, name, last_message_ts)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account, jid) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE wa_chats.name END,
			last_message_ts = max(wa_chats.last_message_ts, excluded.last_message_ts)`,
		account, jid, kind, name, ts)
	if err != nil {
		return fmt.Errorf("whatsapp: keeping chat %s: %w", jid, err)
	}

	return nil
}

// nameGroup keeps a group's name, as WhatsApp lists it or as it changes.
func (m *mirror) nameGroup(ctx context.Context, account, jid, name string) error {
	if name == "" {
		return nil
	}

	_, err := m.db.ExecContext(ctx, `INSERT INTO wa_groups (account, jid, name) VALUES (?, ?, ?)
		ON CONFLICT (account, jid) DO UPDATE SET name = excluded.name`, account, jid, name)
	if err == nil {
		_, err = m.db.ExecContext(ctx, `UPDATE wa_chats SET name = ? WHERE account = ? AND jid = ?`, name, account,
			jid)
	}

	if err != nil {
		return fmt.Errorf("whatsapp: keeping the name of group %s: %w", jid, err)
	}

	return nil
}

// groupName is a group's kept name.
func (m *mirror) groupName(ctx context.Context, account, jid string) string {
	var name string

	err := m.db.QueryRowContext(ctx, `SELECT name FROM wa_groups WHERE account = ? AND jid = ?`, account, jid).
		Scan(&name)
	if err != nil {
		return ""
	}

	return name
}

// chatName is a chat's kept name, a group's or a person's.
func (m *mirror) chatName(ctx context.Context, account, jid string) string {
	var name string

	err := m.db.QueryRowContext(ctx, `SELECT name FROM wa_chats WHERE account = ? AND jid = ?`, account, jid).
		Scan(&name)
	if err != nil {
		return ""
	}

	return name
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	}

	return n
}

// clampChats is how many chats a list gives: up to messaging.MaxChats, every chat for the secretary's list (issue
// #1270); the API and the tools ask for at most maxLimit.
func clampChats(n int) int {
	if n <= 0 {
		return defaultLimit
	}

	return min(n, messaging.MaxChats)
}

func (m *mirror) chats(ctx context.Context, account string, limit int) ([]messaging.Chat, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT jid, kind, name, last_message_ts FROM wa_chats
		WHERE account = ? ORDER BY last_message_ts DESC, jid LIMIT ?`, account, clampChats(limit))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: listing chats: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	out := []messaging.Chat{}

	for rows.Next() {
		var (
			c  messaging.Chat
			ts int64
		)
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &ts); err != nil {
			return nil, err
		}

		c.LastMessageAt = time.Unix(ts, 0).UTC()
		out = append(out, c)
	}

	return out, rows.Err()
}

// ftsPhrase is a search as FTS5 reads it: each word quoted, so that what a person types is never FTS syntax.
func ftsPhrase(search string) string {
	words := strings.Fields(search)
	for i, w := range words {
		words[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
	}

	return strings.Join(words, " ")
}

func (m *mirror) messages(ctx context.Context, account string, q messaging.Query) ([]messaging.Message, error) {
	where := []string{"m.account = ?"}
	args := []any{account}

	if q.Chat != "" {
		where = append(where, "m.chat_jid = ?")
		args = append(args, q.Chat)
	}

	if q.ID != "" {
		where = append(where, "m.msg_id = ?")
		args = append(args, q.ID)
	}

	if !q.Before.IsZero() {
		where = append(where, "m.ts < ?")
		args = append(args, q.Before.Unix())
	}

	if phrase := ftsPhrase(q.Search); phrase != "" {
		where = append(where, "m.rowid IN (SELECT rowid FROM wa_messages_fts WHERE wa_messages_fts MATCH ?)")
		args = append(args, phrase)
	}

	args = append(args, clampLimit(q.Limit), max(q.Offset, 0))

	//nolint:gosec // The conditions are this function's own; every value is a parameter.
	rows, err := m.db.QueryContext(ctx, `SELECT m.msg_id, m.chat_jid,
			CASE WHEN c.name IS NOT NULL AND c.name <> '' THEN c.name ELSE m.chat_name END,
			m.sender_jid, m.sender_name, m.ts, m.from_me, m.text, m.quoted_msg_id, m.media_type, m.media_caption,
			m.filename, m.mime_type, m.edited, m.revoked, m.local_path <> ''
		FROM wa_messages m LEFT JOIN wa_chats c ON c.account = m.account AND c.jid = m.chat_jid
		WHERE `+strings.Join(where, " AND ")+` ORDER BY m.ts DESC, m.rowid DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: listing messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	out := []messaging.Message{}

	for rows.Next() {
		var (
			msg messaging.Message
			ts  int64
		)
		if err := rows.Scan(&msg.ID, &msg.Chat, &msg.ChatName, &msg.Sender, &msg.SenderName, &ts, &msg.FromMe,
			&msg.Text, &msg.QuotedID, &msg.MediaType, &msg.Caption, &msg.FileName, &msg.MimeType, &msg.Edited,
			&msg.Revoked, &msg.MediaKept); err != nil {
			return nil, err
		}

		msg.At = time.Unix(ts, 0).UTC()
		out = append(out, msg)
	}

	return out, rows.Err()
}

// forget removes everything kept of an account, when its link is ended.
func (m *mirror) forget(ctx context.Context, account string) error {
	var errs []error

	for _, table := range []string{"wa_messages", "wa_chats", "wa_groups"} {
		//nolint:gosec // The table names are this function's own.
		if _, err := m.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE account = ?`, account); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
