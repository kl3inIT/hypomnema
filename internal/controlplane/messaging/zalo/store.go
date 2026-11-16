package zalo

// The mirror of every chat (issue #657), shaped as WhatsApp's (internal/controlplane/messaging/whatsapp/store.go,
// after wacli's schema): its own tables in zalo.db, keyed by the account (the owner's Zalo id) so that an account
// linked again keeps its history; every table is prefixed zl_. The session zca-js logs in with is kept here too, as
// whatsmeow keeps its keys in whatsapp.db: the file is private, since a copy of it is the account.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	defaultLimit = 50
	maxLimit     = 500
)

const schema = `
CREATE TABLE IF NOT EXISTS zl_sessions (
	account TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	credentials TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS zl_chats (
	account TEXT NOT NULL,
	id TEXT NOT NULL,
	kind TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	last_message_ts INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, id)
);
CREATE INDEX IF NOT EXISTS zl_chats_last ON zl_chats (account, last_message_ts DESC);

CREATE TABLE IF NOT EXISTS zl_messages (
	rowid INTEGER PRIMARY KEY AUTOINCREMENT,
	account TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	chat_name TEXT NOT NULL DEFAULT '',
	msg_id TEXT NOT NULL,
	cli_msg_id TEXT NOT NULL DEFAULT '',
	sender_id TEXT NOT NULL DEFAULT '',
	sender_name TEXT NOT NULL DEFAULT '',
	ts INTEGER NOT NULL,
	from_me INTEGER NOT NULL,
	kind TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	quoted_msg_id TEXT NOT NULL DEFAULT '',
	attachment_url TEXT NOT NULL DEFAULT '',
	attachment_title TEXT NOT NULL DEFAULT '',
	UNIQUE (account, chat_id, msg_id)
);
CREATE INDEX IF NOT EXISTS zl_messages_chat_ts ON zl_messages (account, chat_id, ts DESC);
CREATE INDEX IF NOT EXISTS zl_messages_ts ON zl_messages (account, ts DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS zl_messages_fts USING fts5(text, attachment_title, chat_name, sender_name);

CREATE TRIGGER IF NOT EXISTS zl_messages_ai AFTER INSERT ON zl_messages BEGIN
	INSERT INTO zl_messages_fts(rowid, text, attachment_title, chat_name, sender_name)
	VALUES (new.rowid, new.text, new.attachment_title, new.chat_name, new.sender_name);
END;
CREATE TRIGGER IF NOT EXISTS zl_messages_ad AFTER DELETE ON zl_messages BEGIN
	DELETE FROM zl_messages_fts WHERE rowid = old.rowid;
END;
CREATE TRIGGER IF NOT EXISTS zl_messages_au AFTER UPDATE ON zl_messages BEGIN
	DELETE FROM zl_messages_fts WHERE rowid = old.rowid;
	INSERT INTO zl_messages_fts(rowid, text, attachment_title, chat_name, sender_name)
	VALUES (new.rowid, new.text, new.attachment_title, new.chat_name, new.sender_name);
END;
`

// upgrades change the tables made by schema, in order; zl_version keeps how many ran, as WhatsApp's mirror does
// (whatsapp/store.go, wa_version).
var upgrades = []string{
	// 1 (issue #669): each message's file and the queue's cursor; each group's walk back through its past; the
	// direct chats' walk.
	`ALTER TABLE zl_messages ADD COLUMN action_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE zl_messages ADD COLUMN media_type TEXT NOT NULL DEFAULT '';
	ALTER TABLE zl_messages ADD COLUMN file_size INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE zl_messages ADD COLUMN mime_type TEXT NOT NULL DEFAULT '';
	ALTER TABLE zl_messages ADD COLUMN local_path TEXT NOT NULL DEFAULT '';
	ALTER TABLE zl_messages ADD COLUMN media_error TEXT NOT NULL DEFAULT '';
	ALTER TABLE zl_chats ADD COLUMN history_cursor TEXT NOT NULL DEFAULT '';
	ALTER TABLE zl_chats ADD COLUMN history_old INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE zl_chats ADD COLUMN history_done INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE IF NOT EXISTS zl_queue (
		account TEXT PRIMARY KEY,
		cursor TEXT NOT NULL DEFAULT '',
		done INTEGER NOT NULL DEFAULT 0
	);`,
}

// mirror reads and writes the kept sessions, chats and messages.
type mirror struct{ db *sql.DB }

func openMirror(ctx context.Context, db *sql.DB) (*mirror, error) {
	const versionTable = `CREATE TABLE IF NOT EXISTS zl_version (version INTEGER NOT NULL);`
	if _, err := db.ExecContext(ctx, schema+versionTable); err != nil {
		return nil, fmt.Errorf("zalo: preparing the tables: %w", err)
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM zl_version`).Scan(&version); err != nil {
		return nil, fmt.Errorf("zalo: reading the tables' version: %w", err)
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
		return fmt.Errorf("zalo: upgrading the tables to version %d: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM zl_version; INSERT INTO zl_version (version) VALUES (?)`,
		version); err != nil {
		return err
	}

	return tx.Commit()
}

// saveSession keeps an account's session, as the link or a renewal of its cookies gives it.
func (m *mirror) saveSession(ctx context.Context, account, name string, credentials []byte) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO zl_sessions (account, name, credentials, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (account) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE zl_sessions.name END,
			credentials = excluded.credentials, updated_at = excluded.updated_at`,
		account, name, string(credentials), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("zalo: keeping the session of %s: %w", account, err)
	}

	return nil
}

// session is an account's kept session and name; ok is false when none is kept.
func (m *mirror) session(ctx context.Context, account string) (credentials []byte, name string, ok bool,
	err error,
) {
	var raw string

	err = m.db.QueryRowContext(ctx, `SELECT credentials, name FROM zl_sessions WHERE account = ?`, account).
		Scan(&raw, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}

	if err != nil {
		return nil, "", false, fmt.Errorf("zalo: reading the session of %s: %w", account, err)
	}

	return []byte(raw), name, true, nil
}

// kept is a message ready to keep. Its file is at mediaURL, named fileName: attachment_url and attachment_title
// in the table, as step 1 named them.
type kept struct {
	account, chat, chatKind, chatName string
	id, cliID, actionID               string
	sender, senderName                string
	at                                time.Time
	fromMe                            bool
	kind, text, quotedID              string
	mediaType, mediaURL, fileName     string
	fileSize                          int64
}

// keep stores a message once, and moves its chat's last message on. added says it was not kept before.
func (m *mirror) keep(ctx context.Context, k kept) (added bool, err error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.

	// Milliseconds, as Zalo stamps its messages, so that paging by Before misses none sent in the same second.
	ts := k.at.UnixMilli()

	res, err := tx.ExecContext(ctx, `INSERT INTO zl_messages (account, chat_id, chat_name, msg_id, cli_msg_id,
		action_id, sender_id, sender_name, ts, from_me, kind, text, quoted_msg_id, media_type, attachment_url,
		attachment_title, file_size, mime_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account, chat_id, msg_id) DO NOTHING`,
		k.account, k.chat, k.chatName, k.id, k.cliID, k.actionID, k.sender, k.senderName, ts, k.fromMe, k.kind,
		k.text, k.quotedID, k.mediaType, k.mediaURL, k.fileName, k.fileSize, mimeOf(k.mediaType, k.fileName))
	if err != nil {
		return false, fmt.Errorf("zalo: keeping message %s: %w", k.id, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO zl_chats (account, id, kind, name, last_message_ts)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account, id) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE zl_chats.name END,
			last_message_ts = max(zl_chats.last_message_ts, excluded.last_message_ts)`,
		k.account, k.chat, k.chatKind, k.chatName, ts)
	if err != nil {
		return false, fmt.Errorf("zalo: keeping chat %s: %w", k.chat, err)
	}

	return n > 0, tx.Commit()
}

// nameChat keeps a chat's name, as Zalo lists its groups.
func (m *mirror) nameChat(ctx context.Context, account, id, kind, name string) error {
	if name == "" {
		return nil
	}

	_, err := m.db.ExecContext(ctx, `INSERT INTO zl_chats (account, id, kind, name) VALUES (?, ?, ?, ?)
		ON CONFLICT (account, id) DO UPDATE SET name = excluded.name`, account, id, kind, name)
	if err == nil {
		// Messages kept before the name was known are found by it too (the search indexes chat_name).
		_, err = m.db.ExecContext(ctx, `UPDATE zl_messages SET chat_name = ? WHERE account = ? AND chat_id = ? AND
			chat_name <> ?`, name, account, id, name)
	}

	if err != nil {
		return fmt.Errorf("zalo: keeping the name of chat %s: %w", id, err)
	}

	return nil
}

// chatName is a chat's kept name.
func (m *mirror) chatName(ctx context.Context, account, id string) string {
	var name string

	if m.db.QueryRowContext(ctx, `SELECT name FROM zl_chats WHERE account = ? AND id = ?`, account, id).
		Scan(&name) != nil {
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

func (m *mirror) chats(ctx context.Context, account string, limit int) ([]messaging.Chat, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id, kind, name, last_message_ts FROM zl_chats
		WHERE account = ? ORDER BY last_message_ts DESC, id LIMIT ?`, account, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("zalo: listing chats: %w", err)
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

		c.LastMessageAt = time.UnixMilli(ts).UTC()
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
		where = append(where, "m.chat_id = ?")
		args = append(args, q.Chat)
	}

	if !q.Before.IsZero() {
		where = append(where, "m.ts < ?")
		args = append(args, q.Before.UnixMilli())
	}

	if phrase := ftsPhrase(q.Search); phrase != "" {
		where = append(where, "m.rowid IN (SELECT rowid FROM zl_messages_fts WHERE zl_messages_fts MATCH ?)")
		args = append(args, phrase)
	}

	args = append(args, clampLimit(q.Limit))

	//nolint:gosec // The conditions are this function's own; every value is a parameter.
	rows, err := m.db.QueryContext(ctx, `SELECT m.msg_id, m.chat_id,
			CASE WHEN c.name IS NOT NULL AND c.name <> '' THEN c.name ELSE m.chat_name END,
			m.sender_id, m.sender_name, m.ts, m.from_me, m.text, m.quoted_msg_id, m.media_type, m.attachment_title,
			m.mime_type, m.local_path <> ''
		FROM zl_messages m LEFT JOIN zl_chats c ON c.account = m.account AND c.id = m.chat_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY m.ts DESC, m.rowid DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("zalo: listing messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	out := []messaging.Message{}

	for rows.Next() {
		var (
			msg messaging.Message
			ts  int64
		)
		if err := rows.Scan(&msg.ID, &msg.Chat, &msg.ChatName, &msg.Sender, &msg.SenderName, &ts, &msg.FromMe,
			&msg.Text, &msg.QuotedID, &msg.MediaType, &msg.FileName, &msg.MimeType, &msg.MediaKept); err != nil {
			return nil, err
		}

		msg.At = time.UnixMilli(ts).UTC()
		out = append(out, msg)
	}

	return out, rows.Err()
}

// forget removes everything kept of an account, its session too, when its link is ended.
func (m *mirror) forget(ctx context.Context, account string) error {
	var errs []error

	for _, table := range []string{"zl_messages", "zl_chats", "zl_sessions", "zl_queue"} {
		//nolint:gosec // The table names are this function's own.
		if _, err := m.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE account = ?`, account); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
