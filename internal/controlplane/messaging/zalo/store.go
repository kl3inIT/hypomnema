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

// mirror reads and writes the kept sessions, chats and messages.
type mirror struct{ db *sql.DB }

func openMirror(ctx context.Context, db *sql.DB) (*mirror, error) {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("zalo: preparing the tables: %w", err)
	}

	return &mirror{db: db}, nil
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

// kept is a message ready to keep.
type kept struct {
	account, chat, chatKind, chatName string
	id, cliID, sender, senderName     string
	at                                time.Time
	fromMe                            bool
	kind, text, quotedID              string
	attachmentURL, attachmentTitle    string
}

// keep stores a message once, and moves its chat's last message on.
func (m *mirror) keep(ctx context.Context, k kept) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.

	ts := k.at.Unix()

	_, err = tx.ExecContext(ctx, `INSERT INTO zl_messages (account, chat_id, chat_name, msg_id, cli_msg_id,
		sender_id, sender_name, ts, from_me, kind, text, quoted_msg_id, attachment_url, attachment_title)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account, chat_id, msg_id) DO NOTHING`,
		k.account, k.chat, k.chatName, k.id, k.cliID, k.sender, k.senderName, ts, k.fromMe, k.kind, k.text,
		k.quotedID, k.attachmentURL, k.attachmentTitle)
	if err != nil {
		return fmt.Errorf("zalo: keeping message %s: %w", k.id, err)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO zl_chats (account, id, kind, name, last_message_ts)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account, id) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE zl_chats.name END,
			last_message_ts = max(zl_chats.last_message_ts, excluded.last_message_ts)`,
		k.account, k.chat, k.chatKind, k.chatName, ts)
	if err != nil {
		return fmt.Errorf("zalo: keeping chat %s: %w", k.chat, err)
	}

	return tx.Commit()
}

// nameChat keeps a chat's name, as Zalo lists its groups.
func (m *mirror) nameChat(ctx context.Context, account, id, kind, name string) error {
	if name == "" {
		return nil
	}

	_, err := m.db.ExecContext(ctx, `INSERT INTO zl_chats (account, id, kind, name) VALUES (?, ?, ?, ?)
		ON CONFLICT (account, id) DO UPDATE SET name = excluded.name`, account, id, kind, name)
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
		where = append(where, "m.chat_id = ?")
		args = append(args, q.Chat)
	}

	if !q.Before.IsZero() {
		where = append(where, "m.ts < ?")
		args = append(args, q.Before.Unix())
	}

	if phrase := ftsPhrase(q.Search); phrase != "" {
		where = append(where, "m.rowid IN (SELECT rowid FROM zl_messages_fts WHERE zl_messages_fts MATCH ?)")
		args = append(args, phrase)
	}

	args = append(args, clampLimit(q.Limit))

	//nolint:gosec // The conditions are this function's own; every value is a parameter.
	rows, err := m.db.QueryContext(ctx, `SELECT m.msg_id, m.chat_id,
			CASE WHEN c.name IS NOT NULL AND c.name <> '' THEN c.name ELSE m.chat_name END,
			m.sender_id, m.sender_name, m.ts, m.from_me, m.text, m.quoted_msg_id, m.attachment_title
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
			&msg.Text, &msg.QuotedID, &msg.FileName); err != nil {
			return nil, err
		}

		msg.At = time.Unix(ts, 0).UTC()
		out = append(out, msg)
	}

	return out, rows.Err()
}

// forget removes everything kept of an account, its session too, when its link is ended.
func (m *mirror) forget(ctx context.Context, account string) error {
	var errs []error

	for _, table := range []string{"zl_messages", "zl_chats", "zl_sessions"} {
		//nolint:gosec // The table names are this function's own.
		if _, err := m.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE account = ?`, account); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
