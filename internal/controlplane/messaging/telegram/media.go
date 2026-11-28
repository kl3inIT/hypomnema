package telegram

// A kept message's file (issue #1184), downloaded when a Bot opens it: Telegram's file references expire, so the
// message is asked again for its file (messages.getMessages, channels.getMessages for a channel's), as mautrix's
// direct download refetches it (pkg/connector/directdownload.go:46-90), and the file is downloaded with gotd's
// downloader from the photo's largest size or the document (media/transfer.go:217-260). It is kept beside
// telegram.db, private as it is, as whatsapp keeps its files (whatsapp/media.go), and answered from there next time.
// A file over 100 MiB is not downloaded (whatsapp's cap, from wacli).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// maxMediaBytes is the largest file downloaded, as whatsapp's.
const maxMediaBytes = 100 << 20

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._@-]`)

// errNoFile is a message the store does not keep, or that has no file.
var errNoFile = errors.New("telegram: no such file")

// Media is a kept message's file, downloaded first when it is not kept yet.
func (a *Account) Media(ctx context.Context, chat, id string) (messaging.Media, error) {
	row, err := a.store.mediaOf(ctx, a.key, chat, id)
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

	if row.mediaType == "" {
		return out, fmt.Errorf("%w: message %s has no file", errNoFile, id)
	}

	client, err := a.liveFull()
	if err != nil {
		return out, err
	}

	loc, size, err := a.fileLocation(ctx, client, chat, id)
	if err != nil {
		return out, err
	}

	if size > maxMediaBytes {
		return out, fmt.Errorf("telegram: the file of message %s is larger than %d MiB", id, maxMediaBytes>>20)
	}

	var buf bytes.Buffer
	if _, err := downloader.NewDownloader().Download(client, loc).Stream(ctx, &buf); err != nil {
		return out, fmt.Errorf("telegram: downloading the file of message %s: %w", id, err)
	}

	k := kept{mediaType: row.mediaType, mime: row.mime, fileName: row.fileName}
	if err := a.keepFile(ctx, chat, id, k, buf.Bytes()); err != nil {
		return out, err
	}

	row, err = a.store.mediaOf(ctx, a.key, chat, id)
	out.Path = row.localPath

	return out, err
}

// fileLocation asks Telegram for a message again and gives where its file is downloaded from, and its size.
func (a *Account) fileLocation(ctx context.Context, client fullAPI, chat, id string,
) (tg.InputFileLocationClass, int64, error) {
	msgID, err := strconv.Atoi(id)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %q is not a message's id", errNoFile, id)
	}

	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}

	var res tg.MessagesMessagesClass

	if strings.HasPrefix(chat, peerChannel+":") {
		peer, err := a.inputPeer(ctx, chat)
		if err != nil {
			return nil, 0, err
		}

		in, _ := peer.(*tg.InputPeerChannel)
		res, err = client.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: in.ChannelID, AccessHash: in.AccessHash}, ID: ids,
		})
	} else {
		res, err = client.MessagesGetMessages(ctx, ids)
	}

	if err != nil {
		return nil, 0, fmt.Errorf("telegram: asking for message %s again: %w", id, err)
	}

	page, ok := res.AsModified()
	if !ok {
		return nil, 0, fmt.Errorf("%w: Telegram no longer has message %s", errNoFile, id)
	}

	for _, mc := range page.GetMessages() {
		m, ok := mc.(*tg.Message)
		if !ok || m.ID != msgID {
			continue
		}

		media, _ := m.GetMedia()

		switch media := media.(type) {
		case *tg.MessageMediaPhoto:
			if photo, ok := media.Photo.(*tg.Photo); ok {
				size, kind := largestSize(photo.Sizes)

				return &tg.InputPhotoFileLocation{
					ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: kind,
				}, size, nil
			}
		case *tg.MessageMediaDocument:
			if doc, ok := media.Document.(*tg.Document); ok {
				return &tg.InputDocumentFileLocation{
					ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference,
				}, doc.Size, nil
			}
		}
	}

	return nil, 0, fmt.Errorf("%w: Telegram no longer has the file of message %s", errNoFile, id)
}

// largestSize is a photo's largest size, its bytes and its type, as mautrix's getLargestPhotoSize picks it.
func largestSize(sizes []tg.PhotoSizeClass) (int64, string) {
	var (
		best  int64
		kind  string
		width int
	)

	for _, s := range sizes {
		switch s := s.(type) {
		case *tg.PhotoSize:
			if s.W > width {
				width, best, kind = s.W, int64(s.Size), s.Type
			}
		case *tg.PhotoSizeProgressive:
			if s.W > width && len(s.Sizes) > 0 {
				width, best, kind = s.W, int64(s.Sizes[len(s.Sizes)-1]), s.Type
			}
		}
	}

	return best, kind
}

// keepFile writes a message's file beside telegram.db and keeps where it is.
func (a *Account) keepFile(ctx context.Context, chat, id string, k kept, data []byte) error {
	path := filepath.Join(a.mediaDir, unsafeName.ReplaceAllString(a.key, "_"),
		unsafeName.ReplaceAllString(chat, "_"), unsafeName.ReplaceAllString(id, "_")+extension(k))
	if err := writePrivate(path, data); err != nil {
		return err
	}

	return a.store.keptFile(ctx, a.key, chat, id, path)
}

// extension is a file's: its sender's name's, or its type's.
func extension(k kept) string {
	if ext := filepath.Ext(k.fileName); ext != "" && !strings.ContainsAny(ext, `/\`) {
		return unsafeName.ReplaceAllString(ext, "_")
	}

	if exts, _ := mime.ExtensionsByType(k.mime); len(exts) > 0 {
		return exts[0]
	}

	return ""
}

// writePrivate writes a file only its owner reads, whole or not at all, as whatsapp's.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("telegram: making the files' directory: %w", err)
	}

	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("telegram: writing a file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("telegram: keeping a file: %w", err)
	}

	return nil
}

// forgetFiles removes the account's kept files, with its link.
func (a *Account) forgetFiles() error {
	return os.RemoveAll(filepath.Join(a.mediaDir, unsafeName.ReplaceAllString(a.key, "_")))
}
