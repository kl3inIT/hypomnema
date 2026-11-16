package whatsapp

// Every kept message's image or file is downloaded (issue #634): as it arrives, live or from a history sync, and
// for the kept ones without a file when the session connects, by a few workers, as wacli's sync --download-media
// and media backfill do (internal/app/media.go: mediaQueue, runMediaWorkers, mediaFilename; docs/media.md).
// whatsmeow checks a file's hashes as it decrypts it; its length must be what the message said. A file over
// 100 MiB is not downloaded (wacli's cap). The files live beside whatsapp.db, private as it is.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

const (
	// maxMediaBytes is the largest file downloaded (wacli docs/media.md).
	maxMediaBytes = 100 << 20
	// mediaWorkers download at once (wacli media backfill --workers).
	mediaWorkers = 4
	// mediaQueueSize is how many downloads wait; one more is left to the next backfill.
	mediaQueueSize = 1024
	// mediaTimeout bounds one download.
	mediaTimeout = 5 * time.Minute
	// backfillBatch is how many kept messages without a file a connection queues.
	backfillBatch = 500
)

// mediaJob is one message whose file to download.
type mediaJob struct{ account, chat, id string }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._@-]`)

// startMedia starts the account's download workers, which end with stop. Each download has its own time within
// base, which outlives the request that made the account.
func (a *account) startMedia(base context.Context) {
	for range mediaWorkers {
		go func() {
			for {
				select {
				case <-a.stop:
					return
				case job := <-a.jobs:
					a.downloadQueued(base, job)
				}
			}
		}()
	}
}

func (a *account) downloadQueued(base context.Context, job mediaJob) {
	ctx, cancel := context.WithTimeout(base, mediaTimeout)
	defer cancel()

	if _, err := a.download(ctx, job); err != nil && !errors.Is(err, errNoMessage) {
		a.log.WarnContext(ctx, "whatsapp: a file could not be downloaded", slog.String("message", job.id),
			slog.String("error", err.Error()))
	}
}

// queueMedia asks for a message's file; a full queue leaves it to the next backfill.
func (a *account) queueMedia(job mediaJob) {
	select {
	case a.jobs <- job:
	default:
	}
}

// backfillMedia queues the kept messages that have no file yet, the newest first (wacli media backfill).
func (a *account) backfillMedia() {
	ctx, cancel := context.WithTimeout(context.Background(), keepTimeout)
	defer cancel()

	jobs, err := a.mirror.pendingMedia(ctx, a.mirrorKey(), backfillBatch)
	if err != nil {
		a.log.WarnContext(ctx, "whatsapp: the files to download could not be listed",
			slog.String("error", err.Error()))

		return
	}

	for _, j := range jobs {
		select {
		case a.jobs <- j:
		case <-a.stop:
			return
		}
	}
}

// Media is a kept message's file, downloaded first when it is not kept yet.
func (a *account) Media(ctx context.Context, chat, id string) (messaging.Media, error) {
	return a.download(ctx, mediaJob{account: a.mirrorKey(), chat: chat, id: id})
}

// download keeps a message's file unless it is kept already, and answers it.
func (a *account) download(ctx context.Context, job mediaJob) (messaging.Media, error) {
	row, err := a.mirror.mediaOf(ctx, job.account, job.chat, job.id)
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

	if row.directPath == "" {
		return out, fmt.Errorf("%w: message %s has no file", errNoMessage, job.id)
	}

	if row.fileLength > maxMediaBytes {
		why := fmt.Sprintf("larger than %d MiB", maxMediaBytes>>20)
		_ = a.mirror.keptMedia(ctx, job.account, job.chat, job.id, "", why)

		return out, fmt.Errorf("whatsapp: the file of message %s is %s", job.id, why)
	}

	data, err := a.client.DownloadMediaWithPath(ctx, row.directPath, row.fileEncHash, row.fileSHA256, row.mediaKey,
		mediaKind(row.mediaType), "", false)
	if err != nil && expired(err) {
		// WhatsApp's servers no longer hold it: the phone is asked to upload it again (issue #651).
		data, err = a.downloadAgain(ctx, job, row)
	}

	if err != nil {
		// A file nobody has is said once; another failure is tried again at the next connection.
		if errors.Is(err, errNotOnPhone) {
			_ = a.mirror.keptMedia(ctx, job.account, job.chat, job.id, "", "neither WhatsApp nor the phone has it")
		}

		return out, fmt.Errorf("whatsapp: downloading the file of message %s: %w", job.id, err)
	}

	if row.fileLength > 0 && int64(len(data)) != row.fileLength {
		return out, fmt.Errorf("whatsapp: the file of message %s is %d bytes, not the %d its message said", job.id,
			len(data), row.fileLength)
	}

	path := filepath.Join(a.mediaDir, unsafeName.ReplaceAllString(job.account, "_"),
		unsafeName.ReplaceAllString(job.chat, "_"), unsafeName.ReplaceAllString(job.id, "_")+extension(row))
	if err := writePrivate(path, data); err != nil {
		return out, err
	}

	if err := a.mirror.keptMedia(ctx, job.account, job.chat, job.id, path, ""); err != nil {
		return out, err
	}

	out.Path = path

	return out, nil
}

// downloadAgain downloads a file the phone uploaded again, from its new path, which is kept.
func (a *account) downloadAgain(ctx context.Context, job mediaJob, row mediaRow) ([]byte, error) {
	path, err := a.askPhone(ctx, job, row)
	if err != nil {
		return nil, err
	}

	if err := a.mirror.movedMedia(ctx, job.account, job.chat, job.id, path); err != nil {
		return nil, err
	}

	return a.client.DownloadMediaWithPath(ctx, path, row.fileEncHash, row.fileSHA256, row.mediaKey,
		mediaKind(row.mediaType), "", false)
}

// mediaKind is whatsmeow's media type of a kept one: a sticker has an image's keys (whatsmeow download.go).
func mediaKind(t string) whatsmeow.MediaType {
	switch t {
	case "video":
		return whatsmeow.MediaVideo
	case "audio":
		return whatsmeow.MediaAudio
	case "document":
		return whatsmeow.MediaDocument
	}

	return whatsmeow.MediaImage
}

// extension is a file's: its sender's name's, or its type's (wacli mediaFilename).
func extension(r mediaRow) string {
	if ext := filepath.Ext(r.fileName); ext != "" && !unsafeName.MatchString(ext) {
		return strings.ToLower(ext)
	}

	if exts, err := mime.ExtensionsByType(strings.Split(r.mime, ";")[0]); err == nil && len(exts) > 0 {
		return exts[0]
	}

	return ".bin"
}

// writePrivate writes a file only its user reads, whole or not at all.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("whatsapp: making the files' directory: %w", err)
	}

	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("whatsapp: writing a file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("whatsapp: keeping a file: %w", err)
	}

	return nil
}

// forgetFiles removes an account's kept files, when its link is ended.
func (a *account) forgetFiles(account string) error {
	if account == "" {
		return nil
	}

	return os.RemoveAll(filepath.Join(a.mediaDir, unsafeName.ReplaceAllString(account, "_")))
}
