package clientapi

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// The music library's routes (issue #910, PR 2's checks 1, 6, 10 and 12).

// musicWorld is a world whose server has a videos Service of its own database and folder, with the free disk a test
// sets.
type musicWorld struct {
	*world

	videos *videos.Service
	dir    string
	free   atomic.Uint64
	// checked is told each time the service reads the free disk, which it does once an upload holds its turn.
	checked chan struct{}
	// exec runs a statement on the videos' database; try runs one and answers its error; pages is its page count.
	exec  func(query string, args ...any)
	try   func(query string, args ...any) error
	pages func() int64
	// owner and bot are the secrets of an owner's token and a Bot's.
	owner, bot string
}

// newMusicWorld is a musicWorld whose videos Service has the config the options give.
func newMusicWorld(t *testing.T, options ...func(*videos.Config)) *musicWorld {
	t.Helper()

	w := &musicWorld{world: newWorld(t), dir: t.TempDir(), checked: make(chan struct{}, 64)}
	w.free.Store(1 << 40)

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	w.try = func(query string, args ...any) error {
		_, err := db.ExecContext(t.Context(), query, args...)

		return err
	}
	w.exec = func(query string, args ...any) { require.NoError(t, w.try(query, args...)) }
	w.pages = func() int64 {
		var n int64
		require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&n))

		return n
	}

	cfg := videos.Config{
		DB: db, Dir: w.dir,
		Tools: func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{}, videos.ErrUnavailable },
		FreeBytes: func(string) (uint64, error) {
			select {
			case w.checked <- struct{}{}:
			default:
			}

			return w.free.Load(), nil
		},
	}
	for _, option := range options {
		option(&cfg)
	}

	w.videos, err = videos.New(cfg)
	require.NoError(t, err)
	w.srv.WithVideos(w.videos)

	_, w.owner, err = w.svc.AddToken(t.Context(), "owner", bearertoken.Owner)
	require.NoError(t, err)
	_, _, err = w.svc.AddToken(t.Context(), "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(t.Context(), "", "")
	require.NoError(t, err)
	w.bot, err = w.svc.AddBotToken(t.Context(), "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	return w
}

// addTrack puts a track of the media type in the library, as an upload leaves it.
func (w *musicWorld) addTrack(t *testing.T, name, mediaType, ext string, body []byte) string {
	t.Helper()

	id := platform.NewID("mus")
	music := filepath.Join(w.dir, "music")
	require.NoError(t, os.MkdirAll(music, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(music, id+"."+ext), body, 0o600))
	w.exec(`INSERT INTO video_music (id, name, file, media_type, length_ms, bytes, created_at)
		VALUES (?, ?, ?, ?, 30000, ?, 1)`, id, name, id+"."+ext, mediaType, len(body))

	return id
}

// countingBody counts its reads.
type countingBody struct {
	r     io.Reader
	reads atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)

	return b.r.Read(p)
}

func (b *countingBody) Close() error { return nil }

// upload is an upload request of size bytes said, with the secret's token, sent as application/octet-stream or as
// the type given ("" for none).
func (w *musicWorld) upload(secret string, size int64, body io.ReadCloser, kind ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(w.t.Context(), http.MethodPost, musicUploadPath+"?name=Nh%E1%BA%A1c", body)
	req.ContentLength = size

	if len(kind) == 0 {
		kind = []string{octetStream}
	}

	if kind[0] != "" {
		req.Header.Set("Content-Type", kind[0])
	}

	if size < 0 {
		req.TransferEncoding = []string{"chunked"}
	}

	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)

	return rec
}

func recordedCode(t *testing.T, rec *httptest.ResponseRecorder) openapi.ErrorCode {
	t.Helper()

	var e openapi.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())

	return e.Code
}

// PR2.1: the route reads no byte of an upload's body before it refuses it: without a token (401), with a Bot's
// (403), without a Content-Length (411), past 64 MB (413), past the quota or the free disk (507), and while another
// upload is received (429, with Retry-After).
func TestAnUploadIsCheckedBeforeItsBodyIsRead(t *testing.T) {
	t.Parallel()

	w := newMusicWorld(t)
	const most = videos.MaxMusicBytes

	for name, c := range map[string]struct {
		secret string
		size   int64
		kind   []string
		before func()
		status int
		code   openapi.ErrorCode
	}{
		"text/plain": {secret: w.owner, size: most, kind: []string{"text/plain"}, status: http.StatusBadRequest,
			code: openapi.ValidationFailed},
		"no Content-Type": {secret: w.owner, size: most, kind: []string{""}, status: http.StatusBadRequest,
			code: openapi.ValidationFailed},
		"no token":           {size: most, status: http.StatusUnauthorized, code: openapi.Unauthorized},
		"a Bot's token":      {secret: w.bot, size: most, status: http.StatusForbidden, code: openapi.Forbidden},
		"no Content-Length":  {secret: w.owner, size: -1, status: http.StatusLengthRequired, code: openapi.InvalidRequest},
		"past 64 MB":         {secret: w.owner, size: most + 1, status: http.StatusRequestEntityTooLarge, code: openapi.RequestTooLarge},
		"the disk is full":   {secret: w.owner, size: most, status: http.StatusInsufficientStorage, code: openapi.InsufficientStorage, before: func() { w.free.Store(400_000_000 + most - 1) }},
		"the quota is spent": {secret: w.owner, size: most, status: http.StatusInsufficientStorage, code: openapi.InsufficientStorage, before: func() { w.fillQuota(t) }},
	} {
		w.free.Store(1 << 40)
		_ = os.RemoveAll(filepath.Join(w.dir, "music"))

		if c.before != nil {
			c.before()
		}

		body := &countingBody{r: bytes.NewReader(make([]byte, 16))}
		rec := w.upload(c.secret, c.size, body, c.kind...)

		assert.Equal(t, c.status, rec.Code, "%s: %s", name, rec.Body.String())
		assert.Equal(t, c.code, recordedCode(t, rec), name)
		assert.Zero(t, body.reads.Load(), "%s: no byte of the body is read", name)
	}

	// A second upload while the first streams.
	w.free.Store(1 << 40)
	_ = os.RemoveAll(filepath.Join(w.dir, "music"))

	pipe, feed := io.Pipe()
	first := make(chan *httptest.ResponseRecorder, 1)

	go func() { first <- w.upload(w.owner, most, pipe) }()

	_, err := feed.Write([]byte("ID3"))
	require.NoError(t, err)

	body := &countingBody{r: bytes.NewReader(make([]byte, 16))}
	rec := w.upload(w.owner, most, body)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Equal(t, openapi.RateLimited, recordedCode(t, rec))
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	assert.Zero(t, body.reads.Load(), "no byte of the second body is read")

	require.NoError(t, feed.CloseWithError(io.ErrUnexpectedEOF))
	assert.NotEqual(t, http.StatusCreated, (<-first).Code)

	// An upload without a name is refused before it is read too.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, musicUploadPath,
		&countingBody{r: bytes.NewReader(nil)})
	req.ContentLength = 10
	req.Header.Set("Authorization", "Bearer "+w.owner)
	req.Header.Set("Content-Type", octetStream)
	named := httptest.NewRecorder()
	w.mux.ServeHTTP(named, req)
	assert.Equal(t, http.StatusBadRequest, named.Code)
}

// fillQuota leaves the library a byte short of a 64 MB track, with a sparse file that takes no disk.
func (w *musicWorld) fillQuota(t *testing.T) {
	t.Helper()

	music := filepath.Join(w.dir, "music")
	require.NoError(t, os.MkdirAll(music, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(music, "full.mp3"), nil, 0o600))
	require.NoError(t, os.Truncate(filepath.Join(music, "full.mp3"), videos.MusicQuotaBytes-videos.MaxMusicBytes+1))
}

// PR2.10 and PR2.6: the library is listed with its quota; a track's file is served as its stored type, never
// sniffed, with ranges; a track is deleted (204), then not found (404); all of it the owner's only.
func TestTheLibraryIsListedServedAndDeleted(t *testing.T) {
	t.Parallel()

	w := newMusicWorld(t)
	ctx := t.Context()
	owner, bot := w.client(w.owner), w.client(w.bot)

	kinds := map[string]string{"mp3": "audio/mpeg", "m4a": "audio/mp4", "wav": "audio/wav", "flac": "audio/flac",
		"ogg": "audio/ogg"}
	ids := map[string]string{}

	for ext, kind := range kinds {
		ids[ext] = w.addTrack(t, "Nhạc "+ext, kind, ext, []byte("0123456789"+ext))
	}

	list, err := owner.ListVideoMusicWithResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, list.JSON200, string(list.Body))
	assert.Len(t, list.JSON200.Tracks, 5)
	assert.Equal(t, int64(videos.MusicQuotaBytes), list.JSON200.QuotaBytes)
	assert.Positive(t, list.JSON200.UsedBytes)

	for ext, kind := range kinds {
		whole, err := owner.ReadVideoMusicAudio(ctx, ids[ext], &openapi.ReadVideoMusicAudioParams{})
		require.NoError(t, err)
		raw, _ := io.ReadAll(whole.Body)
		_ = whole.Body.Close()

		assert.Equal(t, http.StatusOK, whole.StatusCode, ext)
		assert.Equal(t, kind, whole.Header.Get("Content-Type"), ext)
		assert.Equal(t, "nosniff", whole.Header.Get("X-Content-Type-Options"), ext)
		assert.Equal(t, "0123456789"+ext, string(raw), ext)

		part, err := owner.ReadVideoMusicAudio(ctx, ids[ext], &openapi.ReadVideoMusicAudioParams{Range: ptr("bytes=2-4")})
		require.NoError(t, err)
		raw, _ = io.ReadAll(part.Body)
		_ = part.Body.Close()

		assert.Equal(t, http.StatusPartialContent, part.StatusCode, ext)
		assert.Equal(t, "234", string(raw), ext)
		assert.Equal(t, kind, part.Header.Get("Content-Type"), ext)
	}

	refused, err := bot.ListVideoMusicWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	gone, err := bot.DeleteVideoMusicWithResponse(ctx, ids["mp3"])
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, gone.StatusCode())

	deleted, err := owner.DeleteVideoMusicWithResponse(ctx, ids["mp3"])
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, deleted.StatusCode(), string(deleted.Body))

	again, err := owner.DeleteVideoMusicWithResponse(ctx, ids["mp3"])
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, again.StatusCode())

	audio, err := owner.ReadVideoMusicAudio(ctx, ids["mp3"], &openapi.ReadVideoMusicAudioParams{})
	require.NoError(t, err)
	_ = audio.Body.Close()
	assert.Equal(t, http.StatusNotFound, audio.StatusCode)

	after, err := owner.ListVideoMusicWithResponse(ctx)
	require.NoError(t, err)
	assert.Len(t, after.JSON200.Tracks, 4)
	assert.Less(t, after.JSON200.UsedBytes, list.JSON200.UsedBytes)
}

// PR2.12 and PR2.7: GET /videos/looks gains only the default's music, no list of tracks; a music section sent
// without its track is refused by the contract.
func TestTheLooksHaveOnlyTheDefaultsMusic(t *testing.T) {
	t.Parallel()

	w := newMusicWorld(t)
	ctx := t.Context()
	owner := w.client(w.owner)
	id := w.addTrack(t, "Nền", "audio/mpeg", "mp3", []byte("x"))

	w.exec(`UPDATE video_settings SET look = json_set(look, '$.music', json(?))`,
		`{"track":"`+id+`","name":"Nền","length_ms":30000,"gain_db":-4}`)

	looks, err := owner.ListVideoLooksWithResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, looks.JSON200, string(looks.Body))
	require.NotNil(t, looks.JSON200.Default.Music)
	assert.Equal(t, openapi.VideoMusic{Track: id, Name: ptr("Nền"), LengthMs: ptr(int64(30000)), GainDb: -4},
		*looks.JSON200.Default.Music)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(looks.Body, &raw))
	assert.ElementsMatch(t, []string{"presets", "backgrounds", "chromes", "default"}, keys(raw), "no list of tracks")

	gain := -6.0
	for _, body := range []openapi.VideoMake{
		{Take: "tk_0123456789abcdef", Look: &openapi.VideoLookInput{Music: &openapi.VideoMusicInput{GainDb: &gain}}},
	} {
		resp, err := owner.MakeVideoWithResponse(ctx, &openapi.MakeVideoParams{}, body)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode(), string(resp.Body))
	}

	look, err := owner.ChangeVideoLookWithResponse(ctx, "vid_abc", 1, &openapi.ChangeVideoLookParams{IdempotencyKey: "k"},
		openapi.VideoLookChange{Look: &openapi.VideoLookInput{Music: &openapi.VideoMusicInput{GainDb: &gain}}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, look.StatusCode(), string(look.Body))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// rawUpload sends an upload's request line and headers over a connection of its own, and no byte of its body; it
// answers the connection and the status of the answer read within two seconds, 0 when none came.
func (w *musicWorld) rawUpload(t *testing.T, headers string) (net.Conn, int) {
	t.Helper()

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(w.url, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = io.WriteString(conn, "POST "+musicUploadPath+"?name=a HTTP/1.1\r\nHost: x\r\n"+headers+"\r\n")
	require.NoError(t, err)

	return conn, readStatus(t, conn)
}

// readStatus is the status of the answer a connection reads within two seconds, 0 when none came.
func readStatus(t *testing.T, conn net.Conn) int {
	t.Helper()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return 0
	}

	fields := strings.Fields(line)
	require.GreaterOrEqual(t, len(fields), 2, line)

	status, err := strconv.Atoi(fields[1])
	require.NoError(t, err)

	return status
}

// PR2.1 over a real connection (review of #1049): each refusal before the body is answered while the client has
// sent no byte of it, a chunked upload's 411 and a small one's 507 included, which Go's server would otherwise answer
// only once it had read the body.
func TestARefusedUploadIsAnsweredWithoutItsBody(t *testing.T) {
	t.Parallel()

	w := newMusicWorld(t)
	owner := "Authorization: Bearer " + w.owner + "\r\n"
	octet := "Content-Type: application/octet-stream\r\n"
	small := "Content-Length: 1000\r\n"

	for name, c := range map[string]struct {
		headers string
		free    uint64
		status  int
	}{
		"no token":         {headers: octet + small, status: http.StatusUnauthorized},
		"a Bot's token":    {headers: "Authorization: Bearer " + w.bot + "\r\n" + octet + small, status: http.StatusForbidden},
		"text/plain":       {headers: owner + "Content-Type: text/plain\r\n" + small, status: http.StatusBadRequest},
		"no Content-Type":  {headers: owner + small, status: http.StatusBadRequest},
		"chunked":          {headers: owner + octet + "Transfer-Encoding: chunked\r\n", status: http.StatusLengthRequired},
		"past 64 MB":       {headers: owner + octet + "Content-Length: 67108865\r\n", status: http.StatusRequestEntityTooLarge},
		"the disk is full": {headers: owner + octet + small, free: 1, status: http.StatusInsufficientStorage},
	} {
		w.free.Store(cmp.Or(c.free, 1<<40))

		_, status := w.rawUpload(t, c.headers)
		assert.Equal(t, c.status, status, "%s: answered with no byte of the body sent", name)
	}

	// A second upload while the first is received.
	w.free.Store(1 << 40)

	for len(w.checked) > 0 {
		<-w.checked
	}

	first, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(w.url, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	_, err = io.WriteString(first, "POST "+musicUploadPath+"?name=a HTTP/1.1\r\nHost: x\r\n"+owner+octet+
		"Content-Length: 1000\r\n\r\nID3")
	require.NoError(t, err)

	select {
	case <-w.checked:
	case <-time.After(5 * time.Second):
		t.Fatal("the first upload never took its turn")
	}

	_, status := w.rawUpload(t, owner+octet+small)
	assert.Equal(t, http.StatusTooManyRequests, status, "answered with no byte of the body sent")

	require.NoError(t, first.Close())
}

// post uploads a file's bytes through the route over HTTP, as the type given, and answers the status and the track.
func (w *musicWorld) post(t *testing.T, name string, body []byte, kind string) (int, openapi.VideoMusicTrack) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		w.url+musicUploadPath+"?name="+url.QueryEscape(name), bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+w.owner)
	req.Header.Set("Content-Type", kind)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close() //nolint:errcheck // Read whole.

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var track openapi.VideoMusicTrack
	if resp.StatusCode == http.StatusCreated {
		require.NoError(t, json.Unmarshal(raw, &track), string(raw))
	}

	return resp.StatusCode, track
}
