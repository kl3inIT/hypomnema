package telegram

// telegram.db (issue #1184): the session, the update state and the access hashes gotd needs, as mautrix/telegram keeps
// them in its database (pkg/connector/store/upgrades/00-latest.sql: telegram_user_state, telegram_channel_state,
// telegram_access_hash), and the mirror of every chat, shaped as whatsapp.db's wa_* tables (messaging/whatsapp,
// store.go) with its FTS5 index. Every table is prefixed tg_. The mirror is keyed by the account, the owner's
// Telegram user id, so that an account linked again keeps its history.

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
	maxLimit     = messaging.MaxPage
)

// schema is the tables as they were first made; upgrades change them, in order.
const schema = `
CREATE TABLE IF NOT EXISTS tg_sessions (
	account TEXT NOT NULL PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	data BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS tg_state (
	user_id INTEGER NOT NULL PRIMARY KEY,
	pts INTEGER NOT NULL,
	qts INTEGER NOT NULL,
	date INTEGER NOT NULL,
	seq INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tg_channel_state (
	user_id INTEGER NOT NULL,
	channel_id INTEGER NOT NULL,
	pts INTEGER NOT NULL,
	PRIMARY KEY (user_id, channel_id)
);

CREATE TABLE IF NOT EXISTS tg_access_hashes (
	user_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	id INTEGER NOT NULL,
	access_hash INTEGER NOT NULL,
	PRIMARY KEY (user_id, kind, id)
);

CREATE TABLE IF NOT EXISTS tg_dialog_sync (
	account TEXT NOT NULL PRIMARY KEY,
	generation INTEGER NOT NULL DEFAULT 0,
	done INTEGER NOT NULL DEFAULT 0,
	catch_up INTEGER NOT NULL DEFAULT 0,
	offset_peer TEXT NOT NULL DEFAULT '',
	offset_hash INTEGER NOT NULL DEFAULT 0,
	offset_id INTEGER NOT NULL DEFAULT 0,
	offset_date INTEGER NOT NULL DEFAULT 0,
	received INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tg_users (
	account TEXT NOT NULL,
	id TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (account, id)
);

CREATE TABLE IF NOT EXISTS tg_chats (
	account TEXT NOT NULL,
	id TEXT NOT NULL,
	kind TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	listed INTEGER NOT NULL DEFAULT 1,
	last_message_ts INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account, id)
);
CREATE INDEX IF NOT EXISTS tg_chats_last ON tg_chats (account, last_message_ts DESC);

CREATE TABLE IF NOT EXISTS tg_messages (
	rowid INTEGER PRIMARY KEY AUTOINCREMENT,
	account TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	chat_name TEXT NOT NULL DEFAULT '',
	msg_id INTEGER NOT NULL,
	sender_id TEXT NOT NULL DEFAULT '',
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
	UNIQUE (account, chat_id, msg_id)
);
CREATE INDEX IF NOT EXISTS tg_messages_chat_ts ON tg_messages (account, chat_id, ts DESC);
CREATE INDEX IF NOT EXISTS tg_messages_ts ON tg_messages (account, ts DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS tg_messages_fts USING fts5(text, media_caption, filename, chat_name, sender_name);

CREATE TRIGGER IF NOT EXISTS tg_messages_ai AFTER INSERT ON tg_messages BEGIN
	INSERT INTO tg_messages_fts(rowid, text, media_caption, filename, chat_name, sender_name)
	VALUES (new.rowid, new.text, new.media_caption, new.filename, new.chat_name, new.sender_name);
END;
CREATE TRIGGER IF NOT EXISTS tg_messages_ad AFTER DELETE ON tg_messages BEGIN
	DELETE FROM tg_messages_fts WHERE rowid = old.rowid;
END;
CREATE TRIGGER IF NOT EXISTS tg_messages_au AFTER UPDATE ON tg_messages BEGIN
	DELETE FROM tg_messages_fts WHERE rowid = old.rowid;
	INSERT INTO tg_messages_fts(rowid, text, media_caption, filename, chat_name, sender_name)
	VALUES (new.rowid, new.text, new.media_caption, new.filename, new.chat_name, new.sender_name);
END;
`

// upgrades bring the tables to this version, each from the one before; tg_version says which ran, as whatsapp.db's
// (whatsapp/store.go, upgrades).
var upgrades = []string{
	// 1: where a message's file is kept once downloaded or sent (media.go), as wa_messages.local_path.
	`ALTER TABLE tg_messages ADD COLUMN local_path TEXT NOT NULL DEFAULT '';`,
}

// store reads and writes telegram.db.
type store struct{ db *sql.DB }

func openStore(ctx context.Context, db *sql.DB) (*store, error) {
	const versionTable = `CREATE TABLE IF NOT EXISTS tg_version (version INTEGER NOT NULL);`
	if _, err := db.ExecContext(ctx, schema+versionTable); err != nil {
		return nil, fmt.Errorf("telegram: preparing the tables: %w", err)
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM tg_version`).Scan(&version); err != nil {
		return nil, fmt.Errorf("telegram: reading the tables' version: %w", err)
	}

	for v := version; v < len(upgrades); v++ {
		if err := upgrade(ctx, db, v+1, upgrades[v]); err != nil {
			return nil, err
		}
	}

	return &store{db: db}, nil
}

func upgrade(ctx context.Context, db *sql.DB, version int, stmt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.

	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("telegram: upgrading the tables to version %d: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM tg_version; INSERT INTO tg_version (version) VALUES (?)`,
		version); err != nil {
		return err
	}

	return tx.Commit()
}

// errNoSession is a session the file does not hold.
var errNoSession = errors.New("telegram: no session of that account is kept")

// session is an account's kept session: gotd's bytes, and the account as people know it.
func (s *store) session(ctx context.Context, account string) (data []byte, name string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT data, name FROM tg_sessions WHERE account = ?`, account).Scan(&data, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", errNoSession
	}

	if err != nil {
		return nil, "", fmt.Errorf("telegram: reading the session of %s: %w", account, err)
	}

	return data, name, nil
}

func (s *store) keepSession(ctx context.Context, account string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tg_sessions (account, data) VALUES (?, ?)
		ON CONFLICT (account) DO UPDATE SET data = excluded.data`, account, data)
	if err != nil {
		return fmt.Errorf("telegram: keeping the session of %s: %w", account, err)
	}

	return nil
}

// nameSession keeps the account's name as Telegram last gave it.
func (s *store) nameSession(ctx context.Context, account, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tg_sessions SET name = ? WHERE account = ?`, name, account)
	if err != nil {
		return fmt.Errorf("telegram: naming the session of %s: %w", account, err)
	}

	return nil
}

// kept is a message ready to keep, converted from Telegram's (convert.go).
type kept struct {
	chat       string
	id         int
	sender     string
	senderName string
	at         time.Time
	fromMe     bool
	text       string
	quotedID   string
	mediaType  string
	caption    string
	fileName   string
	mime       string
	edited     bool
}

// keepMessage stores a message, or the edit it is, and moves its chat's last message on; a chat not kept yet is
// kept with fallback, the kind its peer type gives until its entity is seen. It says whether the message was new. A
// message kept twice (a backfill repeating a live one) is kept once.
func (s *store) keepMessage(ctx context.Context, account string, k kept, fallback chatInfo) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.

	if _, err := tx.ExecContext(ctx, `INSERT INTO tg_chats (account, id, kind, name, listed) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account, id) DO NOTHING`, account, k.chat, fallback.kind, fallback.name, fallback.listed); err != nil {
		return false, fmt.Errorf("telegram: keeping chat %s: %w", k.chat, err)
	}

	var chatName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM tg_chats WHERE account = ? AND id = ?`, account, k.chat).
		Scan(&chatName); err != nil {
		return false, err
	}

	ts := k.at.Unix()

	res, err := tx.ExecContext(ctx, `INSERT INTO tg_messages (account, chat_id, chat_name, msg_id, sender_id,
		sender_name, ts, from_me, text, quoted_msg_id, media_type, media_caption, filename, mime_type, edited)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account, chat_id, msg_id) DO NOTHING`,
		account, k.chat, chatName, k.id, k.sender, k.senderName, ts, k.fromMe, k.text, k.quotedID, k.mediaType,
		k.caption, k.fileName, k.mime, k.edited)
	if err != nil {
		return false, fmt.Errorf("telegram: keeping message %d: %w", k.id, err)
	}

	added, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	if added == 0 && k.edited {
		if _, err := tx.ExecContext(ctx, `UPDATE tg_messages SET edited = 1, text = ?, media_caption = ?
			WHERE account = ? AND chat_id = ? AND msg_id = ? AND revoked = 0`,
			k.text, k.caption, account, k.chat, k.id); err != nil {
			return false, fmt.Errorf("telegram: keeping the edit of message %d: %w", k.id, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE tg_chats SET last_message_ts = max(last_message_ts, ?)
		WHERE account = ? AND id = ?`, ts, account, k.chat); err != nil {
		return false, err
	}

	return added > 0, tx.Commit()
}

// revoke marks messages deleted, as whatsapp.db keeps a revoked one: its text is dropped. Telegram names a deleted
// message of a direct chat or a basic group by its id alone, which is unique in the account (chat empty); a
// channel's by the channel and the id.
func (s *store) revoke(ctx context.Context, account, chat string, ids []int) error {
	if len(ids) == 0 {
		return nil
	}

	args := []any{account}
	where := "account = ? AND chat_id NOT LIKE 'channel:%'"

	if chat != "" {
		where = "account = ? AND chat_id = ?"
		args = append(args, chat)
	}

	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}

	//nolint:gosec // The conditions are this function's own; every value is a parameter.
	_, err := s.db.ExecContext(ctx, `UPDATE tg_messages SET revoked = 1, text = '', media_caption = '' WHERE `+
		where+` AND msg_id IN (`+strings.Join(marks, ", ")+`)`, args...)
	if err != nil {
		return fmt.Errorf("telegram: keeping deleted messages: %w", err)
	}

	return nil
}

// keepChat keeps a chat as its entity says it is now: its kind, its name and whether it is listed; keepChatAt also
// moves its last message on to at when it is later (a group's creation). A min entity (Telegram's partial copy) never replaces a
// known name with its own, as mautrix keeps a ghost's name from a min user (pkg/connector/userinfo.go:290-292), nor
// whether the account is in the chat: a min channel does not carry the account's membership
// (core.telegram.org/api/min), so it never lists again a channel the account left.
func (s *store) keepChat(ctx context.Context, account string, c chatInfo) error {
	return s.keepChatAt(ctx, account, c, time.Time{})
}

func (s *store) keepChatAt(ctx context.Context, account string, c chatInfo, at time.Time) error {
	var ts int64
	if !at.IsZero() {
		ts = at.Unix()
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO tg_chats (account, id, kind, name, listed, last_message_ts)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (account, id) DO UPDATE SET kind = excluded.kind,
			listed = CASE WHEN ? THEN tg_chats.listed ELSE excluded.listed END,
			name = CASE WHEN excluded.name = '' OR (? AND tg_chats.name <> '') THEN tg_chats.name
				ELSE excluded.name END,
			last_message_ts = max(tg_chats.last_message_ts, excluded.last_message_ts)`,
		account, c.id, c.kind, c.name, c.listed, ts, c.min, c.min)
	if err != nil {
		return fmt.Errorf("telegram: keeping chat %s: %w", c.id, err)
	}

	return nil
}

// refreshChat brings a chat already kept to what its entity says now, and keeps nothing new: an update's entities
// name every member it mentions, and a member is not a chat. A min entity changes neither a known name nor whether
// the chat is listed, as keepChat.
func (s *store) refreshChat(ctx context.Context, account string, c chatInfo) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tg_chats SET kind = ?, listed = CASE WHEN ? THEN listed ELSE ? END,
			name = CASE WHEN ? = '' OR (? AND name <> '') THEN name ELSE ? END
		WHERE account = ? AND id = ?`, c.kind, c.min, c.listed, c.name, c.min, c.name, account, c.id)
	if err != nil {
		return fmt.Errorf("telegram: keeping chat %s: %w", c.id, err)
	}

	return nil
}

// unlist takes a chat off the list, its messages kept: the account left it or lost it.
func (s *store) unlist(ctx context.Context, account, chat string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE tg_chats SET listed = 0 WHERE account = ? AND id = ?`, account,
		chat); err != nil {
		return fmt.Errorf("telegram: taking chat %s off the list: %w", chat, err)
	}

	return nil
}

// dialogCursor is where the listing of an account's dialogs is, kept page by page so that a listing cut short goes
// on from it, as mautrix keeps DialogSyncCursor, its offsets and DialogSyncComplete (pkg/connector/chatsync.go:157-187).
// generation changes with each new listing, so that one running when another starts stops writing.
type dialogCursor struct {
	generation int
	done       bool
	catchUp    bool
	peer       string
	hash       int64
	offsetID   int
	offsetDate int
	received   int
}

// dialogCursor is the account's listing as kept; an account never listed has one not done.
func (s *store) dialogCursor(ctx context.Context, account string) (dialogCursor, error) {
	var c dialogCursor

	err := s.db.QueryRowContext(ctx, `SELECT generation, done, catch_up, offset_peer, offset_hash, offset_id,
		offset_date, received FROM tg_dialog_sync WHERE account = ?`, account).
		Scan(&c.generation, &c.done, &c.catchUp, &c.peer, &c.hash, &c.offsetID, &c.offsetDate, &c.received)
	if errors.Is(err, sql.ErrNoRows) {
		return dialogCursor{}, nil
	}

	if err != nil {
		return dialogCursor{}, fmt.Errorf("telegram: reading where the listing of dialogs is: %w", err)
	}

	return c, nil
}

// restartDialogs starts a new listing from the first page, catching each chat up or not.
func (s *store) restartDialogs(ctx context.Context, account string, catchUp bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tg_dialog_sync (account, generation, catch_up) VALUES (?, 1, ?)
		ON CONFLICT (account) DO UPDATE SET generation = tg_dialog_sync.generation + 1, done = 0,
			catch_up = excluded.catch_up, offset_peer = '', offset_hash = 0, offset_id = 0, offset_date = 0,
			received = 0`, account, catchUp)
	if err != nil {
		return fmt.Errorf("telegram: restarting the listing of dialogs: %w", err)
	}

	return nil
}

// advanceDialogs keeps where the listing is, and says false when another listing started meanwhile.
func (s *store) advanceDialogs(ctx context.Context, account string, c dialogCursor) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO tg_dialog_sync (account, generation, done, catch_up, offset_peer,
			offset_hash, offset_id, offset_date, received) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account) DO UPDATE SET done = excluded.done, offset_peer = excluded.offset_peer,
			offset_hash = excluded.offset_hash, offset_id = excluded.offset_id, offset_date = excluded.offset_date,
			received = excluded.received
		WHERE tg_dialog_sync.generation = excluded.generation`,
		account, c.generation, c.done, c.catchUp, c.peer, c.hash, c.offsetID, c.offsetDate, c.received)
	if err != nil {
		return false, fmt.Errorf("telegram: keeping where the listing of dialogs is: %w", err)
	}

	n, err := res.RowsAffected()

	return n > 0, err
}

// nameUser keeps a person's name, for the messages he sends where Telegram gives his id alone.
func (s *store) nameUser(ctx context.Context, account string, c chatInfo) error {
	if c.name == "" {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO tg_users (account, id, name) VALUES (?, ?, ?)
		ON CONFLICT (account, id) DO UPDATE SET name = CASE WHEN ? AND tg_users.name <> '' THEN tg_users.name
			ELSE excluded.name END`, account, c.id, c.name, c.min)
	if err != nil {
		return fmt.Errorf("telegram: keeping the name of %s: %w", c.id, err)
	}

	return nil
}

// userName is a person's kept name.
func (s *store) userName(ctx context.Context, account, id string) string {
	var name string

	err := s.db.QueryRowContext(ctx, `SELECT name FROM tg_users WHERE account = ? AND id = ?`, account, id).
		Scan(&name)
	if err != nil {
		return ""
	}

	return name
}

// edges are the oldest and the newest message ids kept of a chat; zero when none is.
func (s *store) edges(ctx context.Context, account, chat string) (oldest, newest int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(msg_id), 0), COALESCE(MAX(msg_id), 0) FROM tg_messages
		WHERE account = ? AND chat_id = ?`, account, chat).Scan(&oldest, &newest)
	if err != nil {
		return 0, 0, fmt.Errorf("telegram: reading the messages kept of %s: %w", chat, err)
	}

	return oldest, newest, nil
}

// clampChats is how many chats a list gives: up to messaging.MaxChats, every chat for the secretary's list (issue
// #1270), as whatsapp.db's.
func clampChats(n int) int {
	if n <= 0 {
		return defaultLimit
	}

	return min(n, messaging.MaxChats)
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

// chats lists the account's chats that are listed, the latest first.
func (s *store) chats(ctx context.Context, account string, limit int) ([]messaging.Chat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, name, last_message_ts FROM tg_chats
		WHERE account = ? AND listed = 1 ORDER BY last_message_ts DESC, id LIMIT ?`, account, clampChats(limit))
	if err != nil {
		return nil, fmt.Errorf("telegram: listing chats: %w", err)
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

// messages lists kept messages, the latest first, as whatsapp.db's mirror does.
func (s *store) messages(ctx context.Context, account string, q messaging.Query) ([]messaging.Message, error) {
	where := []string{"m.account = ?"}
	args := []any{account}

	if q.Chat != "" {
		where = append(where, "m.chat_id = ?")
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
		where = append(where, "m.rowid IN (SELECT rowid FROM tg_messages_fts WHERE tg_messages_fts MATCH ?)")
		args = append(args, phrase)
	}

	args = append(args, clampLimit(q.Limit), max(q.Offset, 0))

	//nolint:gosec // The conditions are this function's own; every value is a parameter.
	rows, err := s.db.QueryContext(ctx, `SELECT m.msg_id, m.chat_id,
			CASE WHEN c.name IS NOT NULL AND c.name <> '' THEN c.name ELSE m.chat_name END,
			m.sender_id, m.sender_name, m.ts, m.from_me, m.text, m.quoted_msg_id, m.media_type, m.media_caption,
			m.filename, m.mime_type, m.edited, m.revoked, m.local_path <> ''
		FROM tg_messages m LEFT JOIN tg_chats c ON c.account = m.account AND c.id = m.chat_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY m.ts DESC, m.msg_id DESC, m.rowid DESC LIMIT ? OFFSET ?`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("telegram: listing messages: %w", err)
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

// forget removes everything kept of an account, its session and update state with its mirror, when its link ends.
func (s *store) forget(ctx context.Context, account string, userID int64) error {
	var errs []error

	for _, stmt := range []struct {
		query string
		arg   any
	}{
		{`DELETE FROM tg_messages WHERE account = ?`, account},
		{`DELETE FROM tg_chats WHERE account = ?`, account},
		{`DELETE FROM tg_users WHERE account = ?`, account},
		{`DELETE FROM tg_dialog_sync WHERE account = ?`, account},
		{`DELETE FROM tg_sessions WHERE account = ?`, account},
		{`DELETE FROM tg_state WHERE user_id = ?`, userID},
		{`DELETE FROM tg_channel_state WHERE user_id = ?`, userID},
		{`DELETE FROM tg_access_hashes WHERE user_id = ?`, userID},
	} {
		if _, err := s.db.ExecContext(ctx, stmt.query, stmt.arg); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// mediaRow is what is kept of a message's file.
type mediaRow struct {
	mediaType, mime, fileName, localPath string
}

func (s *store) mediaOf(ctx context.Context, account, chat, id string) (mediaRow, error) {
	var r mediaRow

	err := s.db.QueryRowContext(ctx, `SELECT media_type, mime_type, filename, local_path FROM tg_messages
		WHERE account = ? AND chat_id = ? AND msg_id = ?`, account, chat, id).
		Scan(&r.mediaType, &r.mime, &r.fileName, &r.localPath)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: no message %s is kept in %s", errNoFile, id, chat)
	}

	return r, err
}

// keptFile keeps where a message's file is.
func (s *store) keptFile(ctx context.Context, account, chat, id, path string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tg_messages SET local_path = ? WHERE account = ? AND chat_id = ?
		AND msg_id = ?`, path, account, chat, id)
	if err != nil {
		return fmt.Errorf("telegram: keeping where the file of message %s is: %w", id, err)
	}

	return nil
}
