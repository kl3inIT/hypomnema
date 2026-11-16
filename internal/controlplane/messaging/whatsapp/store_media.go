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
}

func (m *mirror) mediaOf(ctx context.Context, account, chat, id string) (mediaRow, error) {
	var r mediaRow

	err := m.db.QueryRowContext(ctx, `SELECT media_type, mime_type, filename, direct_path, media_key, file_sha256,
			file_enc_sha256, file_length, local_path
		FROM wa_messages WHERE account = ? AND chat_jid = ? AND msg_id = ?`, account, chat, id).
		Scan(&r.mediaType, &r.mime, &r.fileName, &r.directPath, &r.mediaKey, &r.fileSHA256, &r.fileEncHash,
			&r.fileLength, &r.localPath)
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
