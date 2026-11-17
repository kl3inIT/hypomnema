package zalo

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// backupKeyForTests is the AES key the fake bridge says the backup has.
const backupKeyForTests = "0123456789ABCDEF0123456789ABCDEF"

// backupRow is a row of a chat database in a backup the fake bridge makes.
type backupRow struct {
	sender      string
	glb, cli    int64
	typ         int
	at          int64
	text        string
	title, href string
}

// binNet writes BinNet's TLV values: tag, length, bytes, each 4-byte big-endian.
func binNet(title, href string) []byte {
	var b bytes.Buffer

	for _, v := range []struct {
		tag uint32
		val string
	}{{tagTitle, title}, {tagHref, href}} {
		if v.val == "" {
			continue
		}

		_ = binary.Write(&b, binary.BigEndian, v.tag)
		_ = binary.Write(&b, binary.BigEndian, uint32(len(v.val))) //nolint:gosec // A test's short values.
		b.WriteString(v.val)
	}

	return b.Bytes()
}

// writeBackup writes a backup as the phone uploads it: chat databases in an XZ stream after a ZDB4.0 header, in
// 64 KB chunks of AES-256-CBC with a zero IV, the container 4 bytes in (softtyos zdb-cipher.ts).
func writeBackup(path string, gap int, chats map[string][]backupRow) error {
	// The fake bridge makes it outside any test's context.
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "zalo-backup-make-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var names []string

	var blobs [][]byte

	for name, rows := range chats {
		db := filepath.Join(dir, "make-"+name)

		conn, err := sql.Open("sqlite", db)
		if err != nil {
			return err
		}

		if _, err := conn.ExecContext(ctx, `CREATE TABLE ChatContent (SenderId TEXT, CliMsgId INTEGER, GlbMsgId INTEGER,
			MsgStatus INTEGER, MsgType INTEGER, TimeStamp INTEGER, TTL INTEGER, MsgContent TEXT, BinNet BLOB)`); err != nil {
			return err
		}

		for _, r := range rows {
			if _, err := conn.ExecContext(ctx, `INSERT INTO ChatContent VALUES (?, ?, ?, 0, ?, ?, 0, ?, ?)`,
				r.sender, r.cli, r.glb, r.typ, r.at, r.text, binNet(r.title, r.href)); err != nil {
				return err
			}
		}

		if err := conn.Close(); err != nil {
			return err
		}

		blob, err := os.ReadFile(db) //nolint:gosec // The test's own file.
		if err != nil {
			return err
		}

		names, blobs = append(names, name), append(blobs, blob)
	}

	var plain bytes.Buffer
	plain.WriteString("ZDB4.0")
	plain.Write(make([]byte, 8))
	_ = binary.Write(&plain, binary.BigEndian, uint32(len(names))) //nolint:gosec // A test's three files.

	for i, name := range names {
		_ = binary.Write(&plain, binary.BigEndian, uint32(len(name))) //nolint:gosec // A test's short names.
		plain.WriteString(name)
		_ = binary.Write(&plain, binary.BigEndian, uint32(len(blobs[i]))) //nolint:gosec // A test's small databases.
	}

	plain.Write(make([]byte, gap))

	w, err := xz.NewWriter(&plain)
	if err != nil {
		return err
	}

	for _, blob := range blobs {
		if _, err := w.Write(blob); err != nil {
			return err
		}
	}

	if err := w.Close(); err != nil {
		return err
	}

	// The backup's tail: a footer of its own, not zeros, which decrypts to junk after the stream (deplao-builder
	// zdbBackup.ts).
	plain.WriteString("TLV-footer")

	data := plain.Bytes()
	data = append(data, make([]byte, (aes.BlockSize-len(data)%aes.BlockSize)%aes.BlockSize)...)

	block, err := aes.NewCipher([]byte(backupKeyForTests))
	if err != nil {
		return err
	}

	out := []byte{0, 0, 0, 1}

	for off := 0; off < len(data); off += zdbChunk {
		chunk := data[off:min(off+zdbChunk, len(data))]
		enc := make([]byte, len(chunk))
		// The format's own zero IV (softtyos zdb-cipher.ts), which the reader must undo.
		cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(enc, chunk) //nolint:gosec // As above.
		out = append(out, enc...)
	}

	return os.WriteFile(path, out, 0o600)
}

// fakeBackup is the backup the fake bridge's phone uploads: a group whose phone id is 55 (Zalo Web's g1) with a
// photo, the owner's own message and one taken back; a direct chat with 7 (u9); and a group Zalo does not know.
func fakeBackup(dir string) (string, error) {
	path := filepath.Join(dir, "zalo-backup-1.db.crypt")

	return path, writeBackup(path, 0, map[string][]backupRow{
		"group_55.db": {
			{sender: "8", glb: 9001, cli: 1, typ: 3, at: 1_600_000_000_000, title: "Ảnh công trình cũ",
				href: os.Getenv(fakeMediaEnv) + "/old.jpg"},
			{sender: "0", glb: 9002, cli: 2, typ: 0, at: 1_600_000_100_000, text: "Tin cũ của tôi"},
			{sender: "8", glb: 9003, cli: 3, typ: recalled, at: 1_600_000_200_000, text: "đã thu hồi"},
		},
		"7.db":         {{sender: "7", glb: 9101, cli: 4, typ: 0, at: 1_610_000_000_000, text: "Chào anh, hồi năm ngoái"}},
		"group_999.db": {{sender: "8", glb: 9201, cli: 5, typ: 0, at: 1_620_000_000_000, text: "nhóm lạ"}},
	})
}

// fakePhoneIDs are the Zalo Web ids of the fake backup's phone ids; 999 is not known.
var fakePhoneIDs = map[string]string{"u7": "u9", "u8": "200", "g55": "g1"}

// Scenario (issue #697): the owner syncs from his phone. The phone confirms and uploads its backup; every chat of
// it is kept under its Zalo Web id, his own messages as his, a photo's file downloaded, a message taken back left
// out, and a chat Zalo does not know counted as skipped.
func TestASyncFromThePhoneKeepsThePastOfEveryChat(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	require.NoError(t, svc.mirror.saveSession(ctx, "100", "Owner", []byte(`{"imei":"i1"}`)))

	acc, err := svc.Open(ctx, "100")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	live, ok := acc.(*account)
	require.True(t, ok)
	eventually(t, "connected", func() bool { return live.current() != nil })

	syncer, ok := acc.(messaging.PhoneSyncer)
	require.True(t, ok)
	require.NoError(t, syncer.SyncFromPhone(ctx))

	eventually(t, "the sync done", func() bool {
		s := acc.State().PhoneSync

		return s != nil && s.Stage == messaging.SyncDone
	})

	s := acc.State().PhoneSync
	assert.Equal(t, 3, s.Messages)
	assert.Equal(t, 2, s.Chats)
	assert.Equal(t, 1, s.Skipped, "the group Zalo does not know")

	group, err := acc.Messages(ctx, messaging.Query{Chat: "g1", Search: "cũ"})
	require.NoError(t, err)
	require.Len(t, group, 2)
	assert.True(t, group[0].FromMe, "the owner's own message (sender 0)")
	assert.Equal(t, "200", group[1].Sender)

	gone, err := acc.Messages(ctx, messaging.Query{Search: "thu hồi"})
	require.NoError(t, err)
	assert.Empty(t, gone, "a message taken back is left out")

	direct, err := acc.Messages(ctx, messaging.Query{Chat: "u9"})
	require.NoError(t, err)
	require.NotEmpty(t, direct)
	assert.Equal(t, "Chào anh, hồi năm ngoái", direct[len(direct)-1].Text)

	eventually(t, "the photo downloaded", func() bool {
		found, err := acc.Messages(ctx, messaging.Query{Search: "công trình"})

		return err == nil && len(found) == 1 && found[0].MediaKept
	})

	left, err := filepath.Glob(filepath.Join(svc.syncDir, "100", "*"))
	require.NoError(t, err)
	assert.Empty(t, left, "the backup and its databases are removed")

	require.NoError(t, syncer.SyncFromPhone(ctx), "a sync done can be asked again")
}

// The XZ stream may start a few bytes after the header (deplao-builder looks within 64); the chats are read all
// the same.
func TestTheXZStreamIsFoundAFewBytesAfterTheHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gap.db.crypt")
	require.NoError(t, writeBackup(path, 3, map[string][]backupRow{
		"7.db": {{sender: "7", glb: 1, cli: 1, at: 1_600_000_000_000, text: "chào"}},
	}))

	var read []string
	require.NoError(t, eachDatabase(t.Context(), path, []byte(backupKeyForTests), dir,
		func(_ context.Context, file zdbFile, _ string) error {
			read = append(read, file.Name)

			return nil
		}))
	assert.Equal(t, []string{"7.db"}, read)
}

func TestABackupWithTheWrongKeyIsNotRead(t *testing.T) {
	dir := t.TempDir()
	path, err := fakeBackup(dir)
	require.NoError(t, err)

	err = eachDatabase(t.Context(), path, []byte("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"), dir,
		func(context.Context, zdbFile, string) error { return nil })
	require.ErrorIs(t, err, errNotZDB)
}

func TestTheChatOfADatabaseIsReadFromItsName(t *testing.T) {
	for name, want := range map[string]struct {
		id    string
		group bool
		ok    bool
	}{
		"group_55.db": {"55", true, true},
		"7.db":        {"7", false, true},
		"4_1_88.db":   {"88", true, true},
		"3_1_77.db":   {"77", false, true},
		"meta.json":   {"", false, false},
		"a_b.db":      {"", false, false},
	} {
		id, group, ok := chatOf(name)
		assert.Equal(t, want, struct {
			id    string
			group bool
			ok    bool
		}{id, group, ok}, name)
	}
}
