package channels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// The files a message carries (issue #829) reach the Bot as a web owner
// message's attachments do (issue #343): each is downloaded from the service
// (OpenClaw extensions/feishu/src/bot-content.ts, resolveFeishuMediaList),
// put into the chat's folder of attachments in the Bot's folder through the
// helper that writes the Bots' folders, in pieces as the web uploads one, and
// named on the message, where pi reads it. A file that cannot be downloaded
// or kept does not stop the message: it reaches the Bot with a line that
// names the file, as OpenClaw keeps a failed download as a placeholder and
// Hermes writes [Attachment: name].

// noticeUnread is the line a file that could not be read gets in the
// message the Bot reads.
const noticeUnread = "[%s could not be read]"

// readable says whether the Bot can be given a message: it has text, or
// files.
func readable(m Message) bool {
	return len(m.Files) > 0 || (m.IsText && strings.TrimSpace(m.Text) != "")
}

// received keeps the files a message carries in the chat's attachments and
// returns them, with a line for each that could not be.
func (cn *conn) received(ctx context.Context, bot, chatID string, m Message,
) ([]orchestration.ChatAttachment, []string) {
	if len(m.Files) == 0 {
		return nil, nil
	}

	downloader, ok := cn.conn.(Downloader)

	var (
		kept   []orchestration.ChatAttachment
		unread []string
	)

	seen := map[File]bool{}

	for i, f := range m.Files {
		// A file named twice in one message is kept once (OpenClaw resolveFeishuMediaList).
		if seen[File{Key: f.Key, Image: f.Image}] {
			continue
		}

		seen[File{Key: f.Key, Image: f.Image}] = true
		shown := shownFile(f, i)

		if !ok || cn.all.files == nil {
			unread = append(unread, fmt.Sprintf(noticeUnread, shown))

			continue
		}

		a, err := cn.keep(ctx, downloader, bot, chatID, m.ID, f, i)
		if err != nil {
			cn.all.logger.WarnContext(ctx, "a file of a message could not be kept",
				slog.String("event", "channels.file_unread"), slog.String("channel", cn.name),
				slog.String("message_id", m.ID), slog.Any("error", err))

			unread = append(unread, fmt.Sprintf(noticeUnread, shown))

			continue
		}

		kept = append(kept, a)
	}

	return kept, unread
}

// keep downloads one file and puts it in the chat's attachments.
func (cn *conn) keep(ctx context.Context, d Downloader, bot, chatID, messageID string, f File, i int,
) (orchestration.ChatAttachment, error) {
	download, cancel := context.WithTimeout(ctx, mediaTimeout)
	data, err := d.Download(download, messageID, f, workspacefs.MaxUpload)

	cancel()

	switch {
	case err != nil:
		return orchestration.ChatAttachment{}, err
	case len(data) == 0:
		return orchestration.ChatAttachment{}, errors.New("the file is empty")
	}

	name := fileName(f, i, data)
	dir := orchestration.AttachmentsDir(chatID)
	rel := dir + "/" + safeName(messageID) + "-" + name

	if err := cn.all.put(ctx, bot, rel, data); err != nil {
		return orchestration.ChatAttachment{}, err
	}

	return orchestration.ChatAttachment{
		Name: name, Path: rel, Size: int64(len(data)), Type: typeOf(name, data),
	}, nil
}

// put writes a file into a Bot's folder through the helper, as the web
// uploads one (issue #315): the folders it is in, then the file in pieces,
// committed into place.
func (c *Channels) put(ctx context.Context, bot, rel string, data []byte) error {
	ask := func(req workspacefs.Request) (*workspacefs.Done, error) {
		res, err := c.files.Ask(ctx, bot, req)
		if err == nil && res.Error != nil {
			err = res.Error
		}

		if err != nil {
			return nil, err
		}

		return res.Done, nil
	}

	var refused *workspacefs.Error

	// The folders are made one at a time, as the web makes attachments, then attachments/<chat id>; one made
	// before is there.
	parts := strings.Split(path.Dir(rel), "/")
	for n := 1; n <= len(parts); n++ {
		dir := strings.Join(parts[:n], "/")
		if _, err := ask(workspacefs.Request{Op: workspacefs.OpMkdir, Path: dir}); err != nil &&
			(!errors.As(err, &refused) || refused.Code != workspacefs.CodeExists) {
			return fmt.Errorf("making %s: %w", dir, err)
		}
	}

	begun, err := ask(workspacefs.Request{Op: workspacefs.OpUploadBegin, Path: rel})
	if err != nil {
		return fmt.Errorf("uploading %s: %w", rel, err)
	}

	if begun == nil || begun.Upload == "" {
		return fmt.Errorf("uploading %s: the helper named no upload", rel)
	}

	for offset := 0; offset < len(data); offset += workspacefs.MaxChunk {
		piece := data[offset:min(offset+workspacefs.MaxChunk, len(data))]
		if _, err := ask(workspacefs.Request{
			Op: workspacefs.OpUploadChunk, Path: rel, Upload: begun.Upload, Offset: int64(offset), Data: piece,
		}); err != nil {
			_, _ = ask(workspacefs.Request{Op: workspacefs.OpUploadAbort, Path: rel, Upload: begun.Upload})

			return fmt.Errorf("uploading %s: %w", rel, err)
		}
	}

	if _, err := ask(workspacefs.Request{Op: workspacefs.OpUploadCommit, Path: rel, Upload: begun.Upload}); err != nil {
		return fmt.Errorf("uploading %s: %w", rel, err)
	}

	return nil
}

// fileName is the name a file is kept by: the one the service gave, made
// safe for the folder, or, for an image or a file with none, one by its
// place in the message and the kind its bytes are.
func fileName(f File, i int, data []byte) string {
	if name := safeName(f.Name); name != "" && name != "." && name != ".." {
		return name
	}

	kind := "file"
	if f.Image {
		kind = "image"
	}

	ext := ""
	if exts, err := mime.ExtensionsByType(http.DetectContentType(data)); err == nil && len(exts) > 0 {
		ext = exts[0]
	}

	switch ext {
	case ".jfif", ".jpe":
		ext = ".jpg"
	case "":
		ext = ".bin"
	}

	return fmt.Sprintf("%s-%d%s", kind, i+1, ext)
}

// safeName is a name as one line with no folder in it, at most 120 bytes,
// cut on a character.
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == '"' {
			return '_'
		}

		return r
	}, strings.TrimSpace(name))

	for len(name) > 120 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}

	return name
}

// typeOf is a kept file's media type: by its name's extension, else by its
// bytes.
func typeOf(name string, data []byte) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}

	return http.DetectContentType(data)
}

// shownFile names a file in the line that says it could not be read.
func shownFile(f File, i int) string {
	if name := safeName(f.Name); name != "" {
		return name
	}

	if f.Image {
		return fmt.Sprintf("image %d", i+1)
	}

	return fmt.Sprintf("file %d", i+1)
}

// withUnread is a message's text with a line for each file that could not be
// read.
func withUnread(text string, unread []string) string {
	if len(unread) == 0 {
		return text
	}

	lines := strings.Join(unread, "\n")
	if strings.TrimSpace(text) == "" {
		return lines
	}

	return text + "\n" + lines
}
