package zalo

// Every kept message's file is downloaded (issue #669), as it arrives, live or from the past, and the kept ones
// without a file when the session connects, by a few workers: WhatsApp's media.go (#634, after wacli's media
// backfill). Zalo's files are plain links on its CDN, read without the session's cookies as the Hermes Zalo plugin
// reads them (sidecar/media-contract.js, downloadMediaStreamed), and they expire, as zaloclaw notes
// (channel/monitor.ts): so they are taken at once. An image over 20 MiB and another file over 50 MiB is not
// downloaded, as zaloclaw caps them (channel/image-downloader.ts, file-downloader.ts; issue #743). The files live
// beside zalo.db, private as it is. Zalo's phone sends many images as JPEG XL, which models cannot read: such a link is
// fetched as its JPG copy, which the CDN always has, and a JPEG XL file kept before is fetched again (issue #761).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	// maxImageBytes and maxFileBytes cap an image's download and any other file's (zaloclaw).
	maxImageBytes  = 20 << 20
	maxFileBytes   = 50 << 20
	mediaWorkers   = 4
	mediaQueueSize = 1024
	mediaTimeout   = 5 * time.Minute
	// mediaBatch is how many kept messages without a file a connection queues.
	mediaBatch = 500
)

// capOf is the most a file of a type is downloaded up to.
func capOf(mediaType string) int64 {
	if mediaType == "image" {
		return maxImageBytes
	}

	return maxFileBytes
}

// tooLargeError is a file over its cap, said once and not tried again.
type tooLargeError struct{ limit int64 }

func (e tooLargeError) Error() string { return fmt.Sprintf("larger than %d MiB", e.limit>>20) }

// errNoMessage is a message not kept, or one with no file.
var errNoMessage = errors.New("zalo: no such message")

// mediaJob is one message whose file to download; ts is its time, the cursor of the backlog.
type mediaJob struct {
	account, chat, id string
	ts                int64
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._@-]`)

// startMedia starts the account's download workers, which end with ctx.
func (a *account) startMedia(ctx context.Context) {
	a.mediaOnce.Do(func() {
		for range mediaWorkers {
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case job := <-a.jobs:
						a.downloadQueued(ctx, job)
					}
				}
			}()
		}
	})
}

func (a *account) downloadQueued(ctx context.Context, job mediaJob) {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()

	if _, err := a.download(ctx, job); err != nil && !errors.Is(err, errNoMessage) {
		a.svc.log.WarnContext(ctx, "zalo: a file could not be downloaded", slog.String("message", job.id),
			slog.String("error", err.Error()))
	}
}

// queueMedia asks for a message's file; a full queue leaves it to the next connection.
func (a *account) queueMedia(job mediaJob) {
	select {
	case a.jobs <- job:
	default:
	}
}

// backfillMedia queues every kept message that has no file yet, the newest first, a batch at a time.
func (a *account) backfillMedia(ctx context.Context) {
	var before int64

	for {
		jobs, err := a.svc.mirror.pendingMedia(ctx, a.uid, before, mediaBatch)
		if err != nil {
			a.svc.log.WarnContext(ctx, "zalo: the files to download could not be listed",
				slog.String("error", err.Error()))

			return
		}

		for _, j := range jobs {
			select {
			case a.jobs <- j:
			case <-ctx.Done():
				return
			}

			before = j.ts
		}

		if len(jobs) < mediaBatch {
			return
		}
	}
}

// Media is a kept message's file, downloaded first when it is not kept yet, within the time a download has.
func (a *account) Media(ctx context.Context, chat, id string) (messaging.Media, error) {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()

	return a.download(ctx, mediaJob{account: a.uid, chat: chat, id: id})
}

// errLoggedOut is a download asked of an account whose link was ended.
var errLoggedOut = errors.New("zalo: the account was disconnected")

// download keeps a message's file unless it is kept already, and answers it. Logout waits for downloads under way
// (filesMu) and none starts after it.
func (a *account) download(ctx context.Context, job mediaJob) (messaging.Media, error) {
	a.filesMu.RLock()
	defer a.filesMu.RUnlock()

	if a.forgotten {
		return messaging.Media{}, errLoggedOut
	}

	row, err := a.svc.mirror.mediaOf(ctx, job.account, job.chat, job.id)
	if err != nil {
		return messaging.Media{}, err
	}

	out := messaging.Media{MimeType: row.mime, FileName: row.fileName}

	if row.localPath != "" {
		if _, err := os.Stat(row.localPath); err == nil && !isJXL(row.localPath) {
			out.Path = row.localPath

			return out, nil
		}
	}

	if row.url == "" || row.mediaType == "sticker" {
		return out, fmt.Errorf("%w: message %s has no file", errNoMessage, job.id)
	}

	limit := capOf(row.mediaType)
	if row.size > limit {
		why := tooLargeError{limit: limit}.Error()
		_ = a.svc.mirror.keptMedia(ctx, job.account, job.chat, job.id, "", why, "")

		return out, fmt.Errorf("zalo: the file of message %s is %s", job.id, why)
	}

	data, contentType, err := a.svc.fetch(ctx, jpgURL(row.url), limit)
	if err != nil {
		var (
			gone  goneError
			large tooLargeError
		)
		if errors.As(err, &gone) || errors.As(err, &large) {
			// Zalo no longer serves it: said once, not tried again.
			_ = a.svc.mirror.keptMedia(ctx, job.account, job.chat, job.id, "", err.Error(), "")
		}

		return out, fmt.Errorf("zalo: downloading the file of message %s: %w", job.id, err)
	}

	if out.MimeType == "" {
		out.MimeType = contentType
	}

	if err := ctx.Err(); err != nil {
		return out, err
	}

	path := filepath.Join(a.svc.mediaDir, unsafeName.ReplaceAllString(job.account, "_"),
		unsafeName.ReplaceAllString(job.chat, "_"), unsafeName.ReplaceAllString(job.id, "_")+
			extension(row.fileName, out.MimeType))
	if err := writePrivate(path, data); err != nil {
		return out, err
	}

	if err := a.svc.mirror.keptMedia(ctx, job.account, job.chat, job.id, path, "", out.MimeType); err != nil {
		return out, err
	}

	out.Path = path

	return out, nil
}

// jpgURL is a Zalo JPEG XL image's link turned into its JPG copy's, as Zalo PC does where JPEG XL cannot be shown
// (deplao-builder, src/utils/zalo/zaloImageUrl.ts toZaloJpgUrl): /jxl/ becomes /jpg/, .jxl .jpg, and jxlstatus is
// dropped. An end-to-end encrypted link and any other link stay as they are.
func jpgURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || strings.Contains(raw, "?e2esession") || !strings.Contains(u.Path, "/jxl/") ||
		!strings.HasSuffix(strings.ToLower(u.Path), ".jxl") {
		return raw
	}

	toJPG := func(p string) string {
		p = strings.Replace(p, "/jxl/", "/jpg/", 1)

		return p[:len(p)-len(".jxl")] + ".jpg"
	}

	u.Path = toJPG(u.Path)
	if u.RawPath != "" {
		u.RawPath = toJPG(u.RawPath)
	}

	if q := u.Query(); q.Has("jxlstatus") {
		q.Del("jxlstatus")
		u.RawQuery = q.Encode()
	}

	return u.String()
}

// jxlMagic are the two ways a JPEG XL file starts: a bare codestream, or the ISO box container.
var jxlMagic = [][]byte{{0xFF, 0x0A}, {0, 0, 0, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}}

// isJXL is whether a kept file is a JPEG XL image, as Zalo's phone sends them.
func isJXL(path string) bool {
	f, err := os.Open(path) //nolint:gosec // A path this package wrote.
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // Read-only.

	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)

	for _, magic := range jxlMagic {
		if bytes.HasPrefix(head[:n], magic) {
			return true
		}
	}

	return false
}

// goneError is a file Zalo's CDN answers it no longer has (403, 404, 410).
type goneError struct{ status int }

func (e goneError) Error() string { return fmt.Sprintf("Zalo no longer has it (HTTP %d)", e.status) }

// fetch reads a file of Zalo's CDN, at most limit bytes, and answers its type.
func (s *Service) fetch(ctx context.Context, url string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
		return nil, "", errNotPublic
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close() //nolint:errcheck // Read-only.

	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusGone:
		return nil, "", goneError{status: resp.StatusCode}
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}

	if int64(len(data)) > limit {
		return nil, "", tooLargeError{limit: limit}
	}

	return data, strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]), nil
}

// extension is a file's: its sender's name's, or its type's (WhatsApp's extension, after wacli).
func extension(fileName, mimeType string) string {
	if ext := filepath.Ext(fileName); ext != "" && !unsafeName.MatchString(ext) {
		return strings.ToLower(ext)
	}

	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}

	return ".bin"
}

// writePrivate writes a file only its user reads, whole or not at all: each writer has a temporary file of its own,
// so that two downloads of one message never publish a half-written file.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("zalo: making the files' directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".part-*")
	if err != nil {
		return fmt.Errorf("zalo: writing a file: %w", err)
	}

	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}

	if err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("zalo: keeping a file: %w", err)
	}

	return nil
}

// errNotPublic is a link to somewhere other than the public internet.
var errNotPublic = errors.New("zalo: a file's link leads somewhere other than the public internet")

// newHTTP is the client that downloads files: a message's link is the sender's to choose, so it dials only public
// addresses, checked at each connection, redirects included (the browser's egress policy, egress.Public).
func newHTTP() *http.Client {
	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !egress.Public(ap.Addr()) {
				return errNotPublic
			}

			return nil
		},
	}

	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // The standard library's.
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || (req.URL.Scheme != "https" && req.URL.Scheme != "http") {
				return errNotPublic
			}

			return nil
		},
	}
}

// forgetFiles removes an account's kept files, when its link is ended.
func (s *Service) forgetFiles(account string) error {
	if account == "" {
		return nil
	}

	return os.RemoveAll(filepath.Join(s.mediaDir, unsafeName.ReplaceAllString(account, "_")))
}
