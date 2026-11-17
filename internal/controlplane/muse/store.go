package muse

// Everything kept of Muse (issue #782, row 17): muse.db beside the control
// plane's database, as whatsapp.db and zalo.db keep their mirrors
// (messaging/whatsapp/store.go, after wacli's schema; messaging/zalo/store.go),
// with full-text search over each question and answer. It holds each
// account's state as the pool last saw it, which thread belongs to which
// account for good (row 8), each question with its answer, links, outcome and
// the agent that asked, and the files kept. Tables are prefixed mu_.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const schema = `
CREATE TABLE IF NOT EXISTS mu_accounts (
	account TEXT PRIMARY KEY,
	state TEXT NOT NULL DEFAULT '',
	cause TEXT NOT NULL DEFAULT '',
	viewer TEXT NOT NULL DEFAULT '',
	resting INTEGER NOT NULL DEFAULT 0,
	usage TEXT NOT NULL DEFAULT '',
	usage_read_at INTEGER NOT NULL DEFAULT 0,
	checked_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS mu_threads (
	thread TEXT PRIMARY KEY,
	account TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS mu_asks (
	rowid INTEGER PRIMARY KEY AUTOINCREMENT,
	account TEXT NOT NULL,
	thread TEXT NOT NULL DEFAULT '',
	agent TEXT NOT NULL DEFAULT '',
	prompt TEXT NOT NULL,
	answer TEXT NOT NULL DEFAULT '',
	links TEXT NOT NULL DEFAULT '[]',
	approval TEXT NOT NULL DEFAULT '',
	outcome TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	finished_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS mu_asks_thread ON mu_asks (thread, rowid);
CREATE INDEX IF NOT EXISTS mu_asks_time ON mu_asks (created_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS mu_asks_fts USING fts5(prompt, answer);

CREATE TRIGGER IF NOT EXISTS mu_asks_ai AFTER INSERT ON mu_asks BEGIN
	INSERT INTO mu_asks_fts(rowid, prompt, answer) VALUES (new.rowid, new.prompt, new.answer);
END;
CREATE TRIGGER IF NOT EXISTS mu_asks_ad AFTER DELETE ON mu_asks BEGIN
	DELETE FROM mu_asks_fts WHERE rowid = old.rowid;
END;

CREATE TABLE IF NOT EXISTS mu_files (
	rowid INTEGER PRIMARY KEY AUTOINCREMENT,
	ask INTEGER NOT NULL REFERENCES mu_asks (rowid) ON DELETE CASCADE,
	url TEXT NOT NULL,
	path TEXT NOT NULL DEFAULT '',
	mime TEXT NOT NULL DEFAULT '',
	size INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS mu_files_ask ON mu_files (ask);

CREATE TABLE IF NOT EXISTS mu_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// Outcomes of a question (row 17).
const (
	OutcomeAnswered    = "answered"
	OutcomeInterrupted = "interrupted"
	OutcomeUnknown     = "unknown"
	OutcomeAsks        = "asks_permission"
	OutcomeError       = "error"
)

// States of an account (row 7).
const (
	StateSignedIn     = "signed_in"
	StateNeedsSignIn  = "needs_sign_in"
	StateRefused      = "refused"
	StateWorkspace    = "workspace_not_ready"
	StatePageNotReady = "page_not_ready"
	StateNew          = ""
)

const (
	defaultLimit = 20
	maxLimit     = 200
	// toolsKey keeps the tools' switch.
	toolsKey = "tools_on"
)

// store reads and writes muse.db.
type store struct{ db *sql.DB }

func openStore(ctx context.Context, db *sql.DB) (*store, error) {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("muse: preparing the tables: %w", err)
	}

	return &store{db: db}, nil
}

// accountState is what the pool last saw of an account.
type accountState struct {
	State   string
	Cause   string
	Viewer  string
	Resting bool
	Usage   *Usage
	// UsageReadAt is when Settings → Usage was last read.
	UsageReadAt time.Time
	CheckedAt   time.Time
}

func (s *store) state(ctx context.Context, account string) (accountState, error) {
	var (
		st             accountState
		resting        int
		usage          string
		usageAt, check int64
	)

	err := s.db.QueryRowContext(ctx, `SELECT state, cause, viewer, resting, usage, usage_read_at, checked_at
		FROM mu_accounts WHERE account = ?`, account).Scan(&st.State, &st.Cause, &st.Viewer, &resting, &usage,
		&usageAt, &check)
	if errors.Is(err, sql.ErrNoRows) {
		return accountState{}, nil
	}

	if err != nil {
		return accountState{}, fmt.Errorf("muse: reading account %s: %w", account, err)
	}

	st.Resting = resting != 0
	st.UsageReadAt, st.CheckedAt = millis(usageAt), millis(check)

	if usage != "" {
		var u Usage
		if json.Unmarshal([]byte(usage), &u) == nil {
			st.Usage = &u
		}
	}

	return st, nil
}

// setState records an account's state and its cause.
func (s *store) setState(ctx context.Context, account, state, cause, viewer string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO mu_accounts (account, state, cause, viewer, checked_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account) DO UPDATE SET state = excluded.state, cause = excluded.cause,
			viewer = CASE WHEN excluded.viewer = '' THEN mu_accounts.viewer ELSE excluded.viewer END,
			checked_at = excluded.checked_at`,
		account, state, cut(cause, 500), viewer, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("muse: recording account %s: %w", account, err)
	}

	return nil
}

// rest has an account rest, or no more, with why.
func (s *store) rest(ctx context.Context, account string, resting bool, cause string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO mu_accounts (account, resting, cause) VALUES (?, ?, ?)
		ON CONFLICT (account) DO UPDATE SET resting = excluded.resting,
			cause = CASE WHEN excluded.cause = '' THEN mu_accounts.cause ELSE excluded.cause END`,
		account, boolInt(resting), cut(cause, 500))
	if err != nil {
		return fmt.Errorf("muse: recording account %s's rest: %w", account, err)
	}

	return nil
}

// setUsage records what Settings → Usage said.
func (s *store) setUsage(ctx context.Context, account string, u Usage, now time.Time) error {
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO mu_accounts (account, usage, usage_read_at) VALUES (?, ?, ?)
		ON CONFLICT (account) DO UPDATE SET usage = excluded.usage, usage_read_at = excluded.usage_read_at`,
		account, string(raw), now.UnixMilli())
	if err != nil {
		return fmt.Errorf("muse: recording account %s's usage: %w", account, err)
	}

	return nil
}

// forget removes an account's state; its threads and questions stay, as the
// record of what was asked.
func (s *store) forget(ctx context.Context, account string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mu_accounts WHERE account = ?`, account); err != nil {
		return fmt.Errorf("muse: forgetting account %s: %w", account, err)
	}

	return nil
}

// threadAccount is the account a thread belongs to, empty when it is not known.
func (s *store) threadAccount(ctx context.Context, thread string) (string, error) {
	var account string

	err := s.db.QueryRowContext(ctx, `SELECT account FROM mu_threads WHERE thread = ?`, thread).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("muse: reading thread %s: %w", thread, err)
	}

	return account, nil
}

// keepThread records a thread with its account for good: a thread already
// kept keeps its first account.
func (s *store) keepThread(ctx context.Context, thread, account string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO mu_threads (thread, account, created_at) VALUES (?, ?, ?)
		ON CONFLICT (thread) DO NOTHING`, thread, account, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("muse: keeping thread %s: %w", thread, err)
	}

	return nil
}

// Asked is one question kept, with its answer.
type Asked struct {
	ID       int64     `json:"id"`
	Account  string    `json:"account"`
	Thread   string    `json:"thread,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Prompt   string    `json:"prompt"`
	Answer   string    `json:"answer,omitempty"`
	Links    []Link    `json:"links,omitempty"`
	Approval string    `json:"approval,omitempty"`
	Outcome  string    `json:"outcome"`
	Error    string    `json:"error,omitempty"`
	Files    []File    `json:"files,omitempty"`
	At       time.Time `json:"at"`
	Finished time.Time `json:"finished"`
}

// File is a file of an answer, kept or not with why.
type File struct {
	URL   string `json:"url"`
	Path  string `json:"path,omitempty"`
	MIME  string `json:"mime,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Error string `json:"error,omitempty"`
}

// keep records a question and its outcome, with its files.
func (s *store) keep(ctx context.Context, a Asked) (int64, error) {
	links, err := json.Marshal(nonNil(a.Links))
	if err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // Rolling back after a commit does nothing.

	res, err := tx.ExecContext(ctx, `INSERT INTO mu_asks (account, thread, agent, prompt, answer, links, approval,
		outcome, error, created_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Account, a.Thread, a.Agent, a.Prompt, a.Answer, string(links), a.Approval, a.Outcome, cut(a.Error, 2000),
		a.At.UnixMilli(), a.Finished.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("muse: keeping a question: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, f := range a.Files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mu_files (ask, url, path, mime, size, error)
			VALUES (?, ?, ?, ?, ?, ?)`, id, f.URL, f.Path, f.MIME, f.Size, cut(f.Error, 500)); err != nil {
			return 0, fmt.Errorf("muse: keeping a file: %w", err)
		}
	}

	return id, tx.Commit()
}

// Chat is a Muse thread kept, with its last question.
type Chat struct {
	Thread  string    `json:"thread"`
	Account string    `json:"account"`
	Asks    int       `json:"asks"`
	Last    string    `json:"last_prompt"`
	At      time.Time `json:"last_at"`
}

// chats lists the threads, the latest first; with query, those whose
// questions or answers match it.
func (s *store) chats(ctx context.Context, query string, limit int) ([]Chat, error) {
	limit = bounded(limit)

	stmt := `SELECT a.thread, a.account, count(*), max(a.created_at),
			(SELECT prompt FROM mu_asks l WHERE l.thread = a.thread ORDER BY l.rowid DESC LIMIT 1)
		FROM mu_asks a WHERE a.thread != ''`
	args := []any{}

	if query = strings.TrimSpace(query); query != "" {
		stmt += ` AND a.rowid IN (SELECT rowid FROM mu_asks_fts WHERE mu_asks_fts MATCH ?)`

		args = append(args, ftsQuery(query))
	}

	stmt += ` GROUP BY a.thread, a.account ORDER BY max(a.created_at) DESC, a.thread LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("muse: listing chats: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read to the end below.

	var out []Chat

	for rows.Next() {
		var (
			c  Chat
			at int64
		)

		if err := rows.Scan(&c.Thread, &c.Account, &c.Asks, &at, &c.Last); err != nil {
			return nil, err
		}

		c.At = millis(at)
		out = append(out, c)
	}

	return out, rows.Err()
}

// messages lists a thread's questions and answers in order, or with query
// every match, the latest first.
func (s *store) messages(ctx context.Context, thread, query string, limit int) ([]Asked, error) {
	limit = bounded(limit)

	stmt := `SELECT rowid, account, thread, agent, prompt, answer, links, approval, outcome, error, created_at,
		finished_at FROM mu_asks WHERE 1 = 1`
	args := []any{}

	if thread != "" {
		stmt += ` AND thread = ?`

		args = append(args, thread)
	}

	if query = strings.TrimSpace(query); query != "" {
		stmt += ` AND rowid IN (SELECT rowid FROM mu_asks_fts WHERE mu_asks_fts MATCH ?)`

		args = append(args, ftsQuery(query))
	}

	// The last ones, given back in the order they were asked.
	stmt = `SELECT * FROM (` + stmt + ` ORDER BY rowid DESC LIMIT ?) ORDER BY rowid`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("muse: listing messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read to the end below.

	var out []Asked

	for rows.Next() {
		var (
			a          Asked
			links      string
			at, finish int64
		)

		if err := rows.Scan(&a.ID, &a.Account, &a.Thread, &a.Agent, &a.Prompt, &a.Answer, &links, &a.Approval,
			&a.Outcome, &a.Error, &at, &finish); err != nil {
			return nil, err
		}

		_ = json.Unmarshal([]byte(links), &a.Links)
		a.At, a.Finished = millis(at), millis(finish)
		out = append(out, a)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		if out[i].Files, err = s.files(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (s *store) files(ctx context.Context, ask int64) ([]File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT url, path, mime, size, error FROM mu_files WHERE ask = ?
		ORDER BY rowid`, ask)
	if err != nil {
		return nil, fmt.Errorf("muse: listing files: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read to the end below.

	var out []File

	for rows.Next() {
		var f File
		if err := rows.Scan(&f.URL, &f.Path, &f.MIME, &f.Size, &f.Error); err != nil {
			return nil, err
		}

		out = append(out, f)
	}

	return out, rows.Err()
}

// fileKept is a file of the same address kept already, so it is not fetched
// again; its error said once.
func (s *store) fileKept(ctx context.Context, address string) (File, bool, error) {
	var f File

	err := s.db.QueryRowContext(ctx, `SELECT url, path, mime, size, error FROM mu_files WHERE url = ?
		ORDER BY rowid DESC LIMIT 1`, address).Scan(&f.URL, &f.Path, &f.MIME, &f.Size, &f.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}

	return f, err == nil, err
}

// toolsOn reads the tools' switch; on when never set.
func (s *store) toolsOn(ctx context.Context) (bool, error) {
	var value string

	err := s.db.QueryRowContext(ctx, `SELECT value FROM mu_settings WHERE key = ?`, toolsKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("muse: reading the tools' switch: %w", err)
	}

	return value == "1", nil
}

func (s *store) setToolsOn(ctx context.Context, on bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO mu_settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, toolsKey, fmt.Sprint(boolInt(on)))
	if err != nil {
		return fmt.Errorf("muse: setting the tools' switch: %w", err)
	}

	return nil
}

// ftsQuery makes each word of a query a quoted term, so that what an agent
// types is never FTS5 syntax.
func ftsQuery(query string) string {
	var terms []string

	for _, word := range strings.Fields(query) {
		terms = append(terms, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}

	return strings.Join(terms, " ")
}

func bounded(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	}

	return limit
}

func millis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}

	return time.UnixMilli(ms).UTC()
}

func boolInt(b bool) int {
	if b {
		return 1
	}

	return 0
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}
