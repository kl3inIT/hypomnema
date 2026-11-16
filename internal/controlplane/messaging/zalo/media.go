package zalo

// Every kept message's file is downloaded (issue #669), as it arrives, live or from the past, and the kept ones
// without a file when the session connects, by a few workers: WhatsApp's media.go (#634, after wacli's media
// backfill). Zalo's files are plain links on its CDN, read without the session's cookies as the Hermes Zalo plugin
// reads them (sidecar/media-contract.js, downloadMediaStreamed), and they expire, as zaloclaw notes
// (channel/monitor.ts): so they are taken at once. A file over 100 MiB is not downloaded (WhatsApp's and wacli's
// cap). The files live beside zalo.db, private as it is.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	maxMediaBytes  = 100 << 20
	mediaWorkers   = 4
	mediaQueueSize = 1024
	mediaTimeout   = 5 * time.Minute
	// mediaBatch is how many kept messages without a file a connection queues.
	mediaBatch = 500
)

// errNoMessage is a message not kept, or one with no file.
var errNoMessage = errors.New("zalo: no such message")

// mediaJob is one message whose file to download.
type mediaJob struct{ account, chat, id string }

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

// backfillMedia queues the kept messages that have no file yet, the newest first.
func (a *account) backfillMedia(ctx context.Context) {
	jobs, err := a.svc.mirror.pendingMedia(ctx, a.uid, mediaBatch)
	if err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the files to download could not be listed", slog.String("error", err.Error()))

		return
	}

	for _, j := range jobs {
		select {
		case a.jobs <- j:
		case <-ctx.Done():
			return
		}
	}
}

// Media is a kept message's file, downloaded first when it is not kept yet.
func (a *account) Media(ctx context.Context, chat, id string) (messaging.Media, error) {
	return a.download(ctx, mediaJob{account: a.uid, chat: chat, id: id})
}

// download keeps a message's file unless it is kept already, and answers it.
func (a *account) download(ctx context.Context, job mediaJob) (messaging.Media, error) {
	row, err := a.svc.mirror.mediaOf(ctx, job.account, job.chat, job.id)
	if err != nil {
		return messaging.Media{}, err
	}

	out := messaging.Media{MimeType: row.mime, FileName: row.fileName}

	if row.localPath != "" {
		if _, err := os.Stat(row.localPath); err == nil {
			out.Path = row.localPath

			return out, nil
		}
	}

	if row.url == "" || row.mediaType == "sticker" {
		return out, fmt.Errorf("%w: message %s has no file", errNoMessage, job.id)
	}

	if row.size > maxMediaBytes {
		why := fmt.Sprintf("larger than %d MiB", maxMediaBytes>>20)
		_ = a.svc.mirror.keptMedia(ctx, job.account, job.chat, job.id, "", why, "")

		return out, fmt.Errorf("zalo: the file of message %s is %s", job.id, why)
	}

	data, contentType, err := a.svc.fetch(ctx, row.url)
	if err != nil {
		var gone goneError
		if errors.As(err, &gone) {
			// Zalo no longer serves it: said once, not tried again.
			_ = a.svc.mirror.keptMedia(ctx, job.account, job.chat, job.id, "", err.Error(), "")
		}

		return out, fmt.Errorf("zalo: downloading the file of message %s: %w", job.id, err)
	}

	if out.MimeType == "" {
		out.MimeType = contentType
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

// goneError is a file Zalo's CDN answers it no longer has (403, 404, 410).
type goneError struct{ status int }

func (e goneError) Error() string { return fmt.Sprintf("Zalo no longer has it (HTTP %d)", e.status) }

// fetch reads a file of Zalo's CDN, at most maxMediaBytes, and answers its type.
func (s *Service) fetch(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
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

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil {
		return nil, "", err
	}

	if len(data) > maxMediaBytes {
		return nil, "", fmt.Errorf("larger than %d MiB", maxMediaBytes>>20)
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

// writePrivate writes a file only its user reads, whole or not at all.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("zalo: making the files' directory: %w", err)
	}

	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("zalo: writing a file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("zalo: keeping a file: %w", err)
	}

	return nil
}

// forgetFiles removes an account's kept files, when its link is ended.
func (s *Service) forgetFiles(account string) error {
	if account == "" {
		return nil
	}

	return os.RemoveAll(filepath.Join(s.mediaDir, unsafeName.ReplaceAllString(account, "_")))
}
