package telegram

// gotd's storages over telegram.db: the session (session.Storage, gotd session/session.go:136-139), the update state
// (updates.StateStorage, telegram/updates/storage.go:24-41) and the access hashes (ChannelAccessHasher and
// UserAccessHasher, storage.go:43-55), as mautrix/telegram implements them over its database
// (pkg/connector/store/scoped_store.go:38-230): a new state keeps the channels' pts, as mautrix's setStateQuery
// does (scoped_store.go:48-55), where gotd's file storage forgets them (updates/storage_file.go:177-193): gotd sets a
// new state after every general updates.difference (state_difference.go:134), and a restart before a channel's next
// checkpoint would lose the channel. A change of a state not kept is an error (storage.go:21-23).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/updates"
)

// sessionStorage is one account's session.
type sessionStorage struct {
	store   *store
	account string
}

var _ session.Storage = sessionStorage{}

// LoadSession is the kept session, or session.ErrNotFound for an account not linked yet.
func (s sessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	data, _, err := s.store.session(ctx, s.account)
	if errors.Is(err, errNoSession) {
		return nil, session.ErrNotFound
	}

	return data, err
}

func (s sessionStorage) StoreSession(ctx context.Context, data []byte) error {
	return s.store.keepSession(ctx, s.account, data)
}

// stateStorage is the update state and the access hashes of every account, by its user id as gotd gives it.
type stateStorage struct{ db *sql.DB }

var (
	_ updates.StateStorage        = stateStorage{}
	_ updates.ChannelAccessHasher = stateStorage{}
	_ updates.UserAccessHasher    = stateStorage{}
)

// errNoState is changing a state that is not kept, which gotd's storages refuse.
var errNoState = errors.New("telegram: no update state is kept for this account")

func (s stateStorage) GetState(ctx context.Context, userID int64) (updates.State, bool, error) {
	var st updates.State

	err := s.db.QueryRowContext(ctx, `SELECT pts, qts, date, seq FROM tg_state WHERE user_id = ?`, userID).
		Scan(&st.Pts, &st.Qts, &st.Date, &st.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return updates.State{}, false, nil
	}

	if err != nil {
		return updates.State{}, false, fmt.Errorf("telegram: reading the update state: %w", err)
	}

	return st, true, nil
}

// SetState keeps a new state; the channels' pts stay, as mautrix's (scoped_store.go:48-55).
func (s stateStorage) SetState(ctx context.Context, userID int64, st updates.State) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tg_state (user_id, pts, qts, date, seq) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET pts = excluded.pts, qts = excluded.qts, date = excluded.date,
			seq = excluded.seq`, userID, st.Pts, st.Qts, st.Date, st.Seq); err != nil {
		return fmt.Errorf("telegram: keeping the update state: %w", err)
	}

	return nil
}

func (s stateStorage) set(ctx context.Context, userID int64, columns string, values ...any) error {
	//nolint:gosec // The columns are this file's own; every value is a parameter.
	res, err := s.db.ExecContext(ctx, `UPDATE tg_state SET `+columns+` WHERE user_id = ?`,
		append(values, userID)...)
	if err != nil {
		return fmt.Errorf("telegram: keeping the update state: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(errNoState, err)
	}

	return nil
}

func (s stateStorage) SetPts(ctx context.Context, userID int64, pts int) error {
	return s.set(ctx, userID, "pts = ?", pts)
}

func (s stateStorage) SetQts(ctx context.Context, userID int64, qts int) error {
	return s.set(ctx, userID, "qts = ?", qts)
}

func (s stateStorage) SetDate(ctx context.Context, userID int64, date int) error {
	return s.set(ctx, userID, "date = ?", date)
}

func (s stateStorage) SetSeq(ctx context.Context, userID int64, seq int) error {
	return s.set(ctx, userID, "seq = ?", seq)
}

func (s stateStorage) SetDateSeq(ctx context.Context, userID int64, date, seq int) error {
	return s.set(ctx, userID, "date = ?, seq = ?", date, seq)
}

func (s stateStorage) GetChannelPts(ctx context.Context, userID, channelID int64) (int, bool, error) {
	var pts int

	err := s.db.QueryRowContext(ctx, `SELECT pts FROM tg_channel_state WHERE user_id = ? AND channel_id = ?`,
		userID, channelID).Scan(&pts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("telegram: reading a channel's pts: %w", err)
	}

	return pts, true, nil
}

func (s stateStorage) SetChannelPts(ctx context.Context, userID, channelID int64, pts int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tg_channel_state (user_id, channel_id, pts) VALUES (?, ?, ?)
		ON CONFLICT (user_id, channel_id) DO UPDATE SET pts = excluded.pts`, userID, channelID, pts)
	if err != nil {
		return fmt.Errorf("telegram: keeping a channel's pts: %w", err)
	}

	return nil
}

func (s stateStorage) ForEachChannels(ctx context.Context, userID int64,
	f func(ctx context.Context, channelID int64, pts int) error,
) error {
	// The rows are read before f runs: f may write the same tables, which a held connection would wait on.
	channels, err := s.channels(ctx, userID)
	if err != nil {
		return err
	}

	for _, c := range channels {
		if err := f(ctx, c.id, c.pts); err != nil {
			return err
		}
	}

	return nil
}

type channelPts struct {
	id  int64
	pts int
}

func (s stateStorage) channels(ctx context.Context, userID int64) ([]channelPts, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT channel_id, pts FROM tg_channel_state WHERE user_id = ?
		ORDER BY channel_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("telegram: reading the channels' pts: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Read-only.

	var out []channelPts

	for rows.Next() {
		var c channelPts
		if err := rows.Scan(&c.id, &c.pts); err != nil {
			return nil, err
		}

		out = append(out, c)
	}

	return out, rows.Err()
}

func (s stateStorage) accessHash(ctx context.Context, userID int64, kind string, id int64) (int64, bool, error) {
	var hash int64

	err := s.db.QueryRowContext(ctx, `SELECT access_hash FROM tg_access_hashes WHERE user_id = ? AND kind = ?
		AND id = ?`, userID, kind, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("telegram: reading an access hash: %w", err)
	}

	return hash, true, nil
}

func (s stateStorage) setAccessHash(ctx context.Context, userID int64, kind string, id, hash int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tg_access_hashes (user_id, kind, id, access_hash) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, kind, id) DO UPDATE SET access_hash = excluded.access_hash`, userID, kind, id, hash)
	if err != nil {
		return fmt.Errorf("telegram: keeping an access hash: %w", err)
	}

	return nil
}

func (s stateStorage) GetChannelAccessHash(ctx context.Context, userID, channelID int64) (int64, bool, error) {
	return s.accessHash(ctx, userID, peerChannel, channelID)
}

func (s stateStorage) SetChannelAccessHash(ctx context.Context, userID, channelID, accessHash int64) error {
	return s.setAccessHash(ctx, userID, peerChannel, channelID, accessHash)
}

func (s stateStorage) GetUserAccessHash(ctx context.Context, userID, targetUserID int64) (int64, bool, error) {
	return s.accessHash(ctx, userID, peerUser, targetUserID)
}

func (s stateStorage) SetUserAccessHash(ctx context.Context, userID, targetUserID, accessHash int64) error {
	return s.setAccessHash(ctx, userID, peerUser, targetUserID, accessHash)
}
