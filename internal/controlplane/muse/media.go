package muse

// The files of an answer (issue #782, row 17), kept under
// muse-media/<account>/<thread>/, private, as Zalo keeps its own
// (messaging/zalo/media.go): an https address is fetched by the control
// plane with the account's muse.ai cookies, at most 20 MiB an image and 50 MiB
// another file (Zalo's caps, messaging/zalo/media.go:36-38); a blob: address
// lives only in the page and is read there, up to 10 MiB, since one DevTools
// message is capped at 16 MiB with base64 (browser/cdp.go:19-20). A file
// that is not kept says why, once: its address is not fetched again.
// muse.ai/files links expire in about two days (Muse-Chat-MCP README.md:147-154),
// which is why they are kept at once.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
)

const (
	maxImageBytes = 20 << 20
	maxFileBytes  = 50 << 20
	maxBlobBytes  = 10 << 20
	// maxFiles bounds the files kept of one answer.
	maxFiles = 20
)

var (
	errNotPublic = errors.New("not a public address")
	unsafeName   = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// newHTTP is a client that reaches public addresses only, as Zalo's.
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
		Timeout:   5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" {
				return errNotPublic
			}

			return nil
		},
	}
}

// media keeps an answer's files.
type media struct {
	dir   string
	http  *http.Client
	store *store
}

// keepAll keeps each address of an answer, with the account's cookies for
// muse.ai's own and the page for a blob:.
func (m *media) keepAll(
	ctx context.Context, account, thread string, addresses []string, cookies []browser.Cookie, d *driver,
) []File {
	var out []File

	for i, address := range addresses {
		if i == maxFiles {
			break
		}

		if kept, ok, err := m.store.fileKept(ctx, address); err == nil && ok {
			// Said once: a file kept, or one that could not be, is not fetched again.
			if kept.Error != "" {
				kept.Error = ""
				kept.Path = ""
			}

			out = append(out, kept)

			continue
		}

		f := File{URL: address}

		var (
			data []byte
			err  error
		)

		switch {
		case strings.HasPrefix(address, "blob:"):
			data, f.MIME, err = m.blob(ctx, d, address)
		case strings.HasPrefix(address, "https://"):
			data, f.MIME, err = m.fetch(ctx, address, cookies)
		default:
			err = errors.New("not an https or blob address")
		}

		if err == nil {
			f.Path, err = m.write(account, thread, i, f.MIME, data)
			f.Size = int64(len(data))
		}

		if err != nil {
			f.Error = err.Error()
		}

		out = append(out, f)
	}

	return out
}

// fetch downloads an https address, sending the cookies of muse.ai to
// muse.ai only.
func (m *media) fetch(ctx context.Context, address string, cookies []browser.Cookie) ([]byte, string, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, "", err
	}

	if host := u.Hostname(); host == "muse.ai" || strings.HasSuffix(host, ".muse.ai") {
		for _, c := range cookies {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // Read or failed.

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("downloading: the server answered %d", resp.StatusCode)
	}

	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	limit := capOf(kind)

	if resp.ContentLength > limit {
		return nil, kind, tooLarge(limit)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, kind, fmt.Errorf("downloading: %w", err)
	}

	if int64(len(data)) > limit {
		return nil, kind, tooLarge(limit)
	}

	return data, kind, nil
}

// blob reads a blob: address in the page.
func (m *media) blob(ctx context.Context, d *driver, address string) ([]byte, string, error) {
	if d == nil {
		return nil, "", errors.New("the page that held it is gone")
	}

	var got struct {
		OK    bool   `json:"ok"`
		MIME  string `json:"mime"`
		Size  int64  `json:"size"`
		Data  string `json:"data"`
		Error string `json:"error"`
	}

	if err := d.eval(ctx, readBlob(address, maxBlobBytes), &got); err != nil {
		return nil, "", err
	}

	switch {
	case !got.OK && got.Size > maxBlobBytes:
		return nil, got.MIME, tooLarge(maxBlobBytes)
	case !got.OK:
		return nil, got.MIME, fmt.Errorf("reading it in the page: %s", got.Error)
	}

	data, err := base64.StdEncoding.DecodeString(got.Data)
	if err != nil || int64(len(data)) > capOf(got.MIME) {
		return nil, got.MIME, errors.New("the page gave what is not the file")
	}

	return data, got.MIME, nil
}

// write keeps a file under muse-media/<account>/<thread>/, private.
func (m *media) write(account, thread string, index int, kind string, data []byte) (string, error) {
	if thread == "" {
		thread = "no-thread"
	}

	dir := filepath.Join(m.dir, unsafeName.ReplaceAllString(account, "_"), unsafeName.ReplaceAllString(thread, "_"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	ext := ".bin"
	if exts, _ := mime.ExtensionsByType(kind); len(exts) > 0 {
		ext = exts[0]
	}

	name := fmt.Sprintf("%d-%d%s", time.Now().UnixMilli(), index, ext)
	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}

	return path, nil
}

func capOf(kind string) int64 {
	if strings.HasPrefix(kind, "image/") {
		return maxImageBytes
	}

	return maxFileBytes
}

func tooLarge(limit int64) error {
	return fmt.Errorf("over %d MiB, not kept", limit>>20)
}
