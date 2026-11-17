package zalo

// The backup the owner's phone uploads when he syncs it (issue #697): a ZDB4.0 container, encrypted. Its format
// is learned from softtyos/zca-js (MIT, src/crypto/zdb-cipher.ts: the 64 KB AES-256-CBC chunks with a zero IV,
// the header) and zlog's worker_sync (pkg/sqlite/{zdb_parser,parser,binnet}.go, internal/crypto/xz.go, read as a
// specification: the XZ stream of the files in the header's order, the ChatContent table, BinNet's TLV tags).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

const (
	// zdbChunk is the size of each independently encrypted chunk (softtyos ZDB_CHUNK_SIZE).
	zdbChunk = 0x10000
	// zdbMaxFiles bounds the header's count of files (softtyos parseZdbHeader: 100000).
	zdbMaxFiles = 100000
	// zdbMaxName bounds a file's name in the header (softtyos: 4096).
	zdbMaxName = 4096
	// xzTrailing is the xz reader's word for bytes after the one stream it read whole.
	xzTrailing = "xz: unexpected data after stream"
)

var (
	zdbMagic    = []byte("ZDB4.0")
	sqliteMagic = []byte("SQLite format 3\x00")
	// errNotZDB is a backup that does not decrypt to a ZDB4.0 container.
	errNotZDB = errors.New("zalo: the backup is not a ZDB4.0 container, or its key is wrong")
)

// zdbFile is one of the container's files: a chat's SQLite database, <uid>.db or group_<id>.db.
type zdbFile struct {
	Name string
	Size int64
}

// chunkReader decrypts a backup a chunk at a time, each with its own AES-256-CBC and a zero IV.
type chunkReader struct {
	src   io.Reader
	block cipher.Block
	buf   []byte
	plain []byte
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.plain) == 0 {
		n, err := io.ReadFull(c.src, c.buf)
		n -= n % aes.BlockSize
		if n == 0 {
			if err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
				err = io.EOF
			}

			return 0, err
		}

		out := make([]byte, n)
		cipher.NewCBCDecrypter(c.block, make([]byte, aes.BlockSize)).CryptBlocks(out, c.buf[:n])
		c.plain = out
	}

	n := copy(p, c.plain)
	c.plain = c.plain[n:]

	return n, nil
}

// openBackup decrypts the backup at path with its key. The container starts at the file's start or 4 bytes in;
// the one whose first chunk starts with the magic is it (softtyos decryptZdbBuffer).
func openBackup(path string, key []byte) (io.Reader, io.Closer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("zalo: the backup's key: %w", err)
	}

	for _, start := range []int64{0, 4} {
		f, err := os.Open(path) //nolint:gosec // The backup the control plane wrote beside zalo.db.
		if err != nil {
			return nil, nil, fmt.Errorf("zalo: opening the backup: %w", err)
		}

		if _, err := f.Seek(start, io.SeekStart); err != nil {
			_ = f.Close()

			return nil, nil, fmt.Errorf("zalo: reading the backup: %w", err)
		}

		r := bufio.NewReaderSize(&chunkReader{src: f, block: block, buf: make([]byte, zdbChunk)}, zdbChunk)
		if head, err := r.Peek(len(zdbMagic)); err == nil && bytes.Equal(head, zdbMagic) {
			return r, f, nil
		}

		_ = f.Close()
	}

	return nil, nil, errNotZDB
}

// readHeader reads the container's header: the magic, at 14 the count of files, then each file's name and size,
// all big-endian 32-bit (softtyos parseZdbHeader; zlog reads the same bytes as 16-bit halves).
func readHeader(r io.Reader) ([]zdbFile, error) {
	head := make([]byte, 18)
	if _, err := io.ReadFull(r, head); err != nil || !bytes.Equal(head[:len(zdbMagic)], zdbMagic) {
		return nil, errNotZDB
	}

	count := binary.BigEndian.Uint32(head[14:18])
	if count > zdbMaxFiles {
		return nil, fmt.Errorf("zalo: the backup says it holds %d files", count)
	}

	files := make([]zdbFile, 0, count)
	word := make([]byte, 4)

	for range count {
		if _, err := io.ReadFull(r, word); err != nil {
			return nil, fmt.Errorf("zalo: the backup's header is cut short: %w", err)
		}

		size := binary.BigEndian.Uint32(word)
		if size > zdbMaxName {
			return nil, fmt.Errorf("zalo: a name in the backup's header is %d bytes", size)
		}

		name := make([]byte, size)
		if _, err := io.ReadFull(r, name); err != nil {
			return nil, fmt.Errorf("zalo: the backup's header is cut short: %w", err)
		}

		if _, err := io.ReadFull(r, word); err != nil {
			return nil, fmt.Errorf("zalo: the backup's header is cut short: %w", err)
		}

		base := filepath.Base(strings.ReplaceAll(strings.TrimRight(string(name), "\x00"), "\\", "/"))
		if base == "." || base == ".." || base == "/" {
			return nil, fmt.Errorf("zalo: an unsafe name in the backup's header: %q", name)
		}

		files = append(files, zdbFile{Name: base, Size: int64(binary.BigEndian.Uint32(word))})
	}

	return files, nil
}

// eachDatabase writes each of the backup's chat databases in turn to a file in dir and hands it to fn, then
// removes it: the XZ stream after the header holds the files one after the other, in the header's order (zlog
// ExtractDatabases). A file that is not SQLite is passed over.
func eachDatabase(ctx context.Context, path string, key []byte, dir string,
	fn func(ctx context.Context, file zdbFile, db string) error,
) error {
	r, closer, err := openBackup(path, key)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	files, err := readHeader(r)
	if err != nil {
		return err
	}

	// One stream, then the chunks' padding (zlog looks for its footer instead).
	unxz, err := xz.ReaderConfig{SingleStream: true}.NewReader(r)
	if err != nil {
		return fmt.Errorf("zalo: the backup's XZ stream: %w", err)
	}

	src := ctxReader{ctx: ctx, r: unxz}

	for _, file := range files {
		if err := oneDatabase(ctx, src, file, dir, fn); err != nil {
			return err
		}
	}

	// The rest of the stream is read to its end, which checks its index and footer: a stream cut short or corrupt
	// fails here. What follows the stream is the chunks' AES padding, which the reader reports once the stream was
	// read whole (ulikunitz/xz reader.go, errUnexpectedData, unexported).
	if _, err := io.Copy(io.Discard, src); err != nil && err.Error() != xzTrailing {
		return fmt.Errorf("zalo: the backup's XZ stream: %w", err)
	}

	return nil
}

// ctxReader ends a read of a large database when the import is canceled.
type ctxReader struct {
	ctx context.Context //nolint:containedctx // Read has no context of its own.
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}

	return c.r.Read(p)
}

func oneDatabase(ctx context.Context, r io.Reader, file zdbFile, dir string,
	fn func(ctx context.Context, file zdbFile, db string) error,
) error {
	tmp, err := os.CreateTemp(dir, "zalo-chat-*.db")
	if err != nil {
		return fmt.Errorf("zalo: a file for the backup's %s: %w", file.Name, err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	n, err := io.CopyN(tmp, r, file.Size)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return fmt.Errorf("zalo: the backup's %s is cut short at %d of %d bytes: %w", file.Name, n, file.Size, err)
	}

	head := make([]byte, len(sqliteMagic))
	f, err := os.Open(tmp.Name())
	if err != nil {
		return fmt.Errorf("zalo: reading the backup's %s: %w", file.Name, err)
	}

	_, rerr := io.ReadFull(f, head)
	_ = f.Close()

	if rerr != nil || !bytes.Equal(head, sqliteMagic) {
		return nil
	}

	return fn(ctx, file, tmp.Name())
}

// phoneRow is a row of a chat database's ChatContent table (zlog parser.go, parseMessages).
type phoneRow struct {
	Sender    string
	CliID     int64
	GlobalID  int64
	Type      int
	At        int64
	Text      string
	BinNet    []byte
	RowNumber int64
}

// eachRow reads a chat database's messages, the oldest first.
func eachRow(ctx context.Context, path string, fn func(phoneRow) error) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("zalo: opening a chat of the backup: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, `SELECT rowid, SenderId, CliMsgId, GlbMsgId, MsgType, TimeStamp, MsgContent, BinNet
		FROM ChatContent ORDER BY TimeStamp`)
	if err != nil {
		return fmt.Errorf("zalo: reading a chat of the backup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			r                  phoneRow
			sender, text       sql.NullString
			cli, glb, typ, ats sql.NullInt64
		)

		if err := rows.Scan(&r.RowNumber, &sender, &cli, &glb, &typ, &ats, &text, &r.BinNet); err != nil {
			continue
		}

		r.Sender, r.CliID, r.GlobalID, r.Type, r.At, r.Text = sender.String, cli.Int64, glb.Int64, int(typ.Int64),
			ats.Int64, text.String

		if err := fn(r); err != nil {
			return err
		}
	}

	return rows.Err()
}

// BinNet's tags this reads (zlog binnet.go): each value is the 4-byte tag, a 4-byte big-endian length, the bytes.
const (
	tagTitle  = 0x2F
	tagHref   = 0x30
	tagParams = 0x2E
)

// binNetValue is the first value of tag in a BinNet, found as zlog finds it: by its 4 bytes.
func binNetValue(b []byte, tag uint32) string {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], tag)

	i := bytes.Index(b, t[:])
	if i < 0 || i+8 > len(b) {
		return ""
	}

	n := int(binary.BigEndian.Uint32(b[i+4 : i+8]))
	if n == 0 || n > 50000 || i+8+n > len(b) {
		return ""
	}

	return string(b[i+8 : i+8+n])
}
