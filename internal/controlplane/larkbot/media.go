package larkbot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// A Bot sends a file into its Lark chat (issue #312) with a line of its reply
// that is MEDIA:<path>, which its built-in instructions teach it, as OpenClaw
// (src/media/parse-output.ts, src/agents/system-prompt-messaging.ts) and
// Hermes (agent/prompt_builder.py) have their agents ask. The line is taken
// out of the text shown. Only a file in the Bot's own folder is sent, read
// through the helper that reads the Bots' folders for the Files panel (issue
// #255), which keeps every path inside the folder after its links are
// followed (OpenClaw src/media/local-roots.ts: a session reads only its own
// workspace). It is sent after the reply's text, a file message in the same
// chat (OpenClaw extensions/feishu/src/media.ts; Hermes
// plugins/platforms/feishu/adapter.py). A file that cannot be sent gets one
// short line in the chat that says why.

// Files reads a Bot's folder: the helper `hypomnema files serve`, run as the
// user the Bots' files belong to. Root is where a Bot's folder is.
type Files interface {
	Ask(ctx context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error)
	Root(bot string) string
}

// Attachment is a file sent into a Lark chat: its name as Lark shows it, the
// file_type Lark files it under, and its bytes.
type Attachment struct {
	Name string
	Kind string
	Data []byte
}

// noticeNotSent is the line a file that was not sent gets: its name and why.
const noticeNotSent = "%s was not sent: %s."

// mediaLine is a line that asks for a file: MEDIA: at its start, which
// consumes the whole line (OpenClaw, parse-output.ts MEDIA_TOKEN_RE).
var mediaLine = regexp.MustCompile(`(?i)^\s*MEDIA:\s*(.*?)\s*$`)

// mediaOf splits a reply into the text shown and the paths of the files it
// asks for. A line in a fenced code block is text, as OpenClaw leaves a
// directive in a code block alone. A reply that asks for none is as it was.
func mediaOf(text string) (shown string, paths []string) {
	if !strings.Contains(strings.ToLower(text), "media:") {
		return text, nil
	}

	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	fence := ""
	asked := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"):
			fence = "```"
		case strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
		default:
			if m := mediaLine.FindStringSubmatch(line); m != nil {
				asked = true

				if p := cleanPath(m[1]); p != "" {
					paths = append(paths, p)
				}

				continue
			}
		}

		kept = append(kept, line)
	}

	if !asked {
		return text, nil
	}

	return strings.TrimSpace(strings.Join(kept, "\n")), paths
}

// cleanPath is a path without the quotes, backticks and brackets a model
// wraps it in, and without file:// (OpenClaw, parse-output.ts cleanCandidate
// and normalizeMediaSource).
func cleanPath(raw string) string {
	p := strings.TrimLeft(raw, "`\"'[{(<")
	p = strings.TrimRight(p, "`\"'\\})],>")
	p = strings.TrimPrefix(p, "file://")

	return strings.TrimSpace(p)
}

// attachment reads a file a Bot asked to send, or says why it is not sent.
// The path is relative to the Bot's folder, where its pi works, or absolute
// inside it. It is read through the helper, which refuses a path that climbs
// out of the folder or whose links lead out; an empty file, one over 30 MB
// and one that is not there are refused too (OpenClaw media.ts,
// assertFeishuUploadWithinEnvelope).
func (c *Channels) attachment(ctx context.Context, bot, asked string) (Attachment, string) {
	if c.files == nil {
		return Attachment{}, "files cannot be sent from this control plane (its Bots' folders are not set up)"
	}

	rel, ok := relativeTo(c.files.Root(bot), asked)
	if !ok {
		return Attachment{}, "only a file in " + bot + "'s own folder can be sent"
	}

	res, err := c.files.Ask(ctx, bot, workspacefs.Request{
		Op: workspacefs.OpRead, Path: rel, Raw: true, Limit: workspacefs.MaxSend,
	})
	if err == nil && res.Error != nil {
		err = res.Error
	}

	var refused *workspacefs.Error

	switch {
	case errors.As(err, &refused):
		return Attachment{}, refusalOf(refused.Code, bot)
	case err != nil:
		c.logger.WarnContext(ctx, "a file a Bot asked to send was not read", slog.String("event", "larkbot.file_failed"),
			slog.String("bot", bot), slog.Any("error", err))

		return Attachment{}, "it could not be read"
	case res.File == nil || len(res.File.Data) == 0:
		return Attachment{}, "it is empty"
	}

	name := uploadName(path.Base(res.File.Path))

	return Attachment{Name: name, Kind: fileType(name, res.File.Data), Data: res.File.Data}, ""
}

// relativeTo is a path the Bot named as one relative to its folder: as it is
// when it is relative, cut from the folder's path when it is absolute and in
// the folder. Anything else is not a file of the folder.
func relativeTo(root, asked string) (string, bool) {
	switch {
	case strings.Contains(asked, "://"), strings.HasPrefix(asked, "~"):
		return "", false
	case strings.HasPrefix(asked, "/"):
		root = strings.TrimSuffix(root, "/")
		if root == "" || !strings.HasPrefix(asked, root+"/") {
			return "", false
		}

		asked = strings.TrimPrefix(asked, root+"/")
	}

	asked = path.Clean(strings.TrimPrefix(asked, "./"))
	if asked == "." || asked == "" {
		return "", false
	}

	return asked, true
}

// refusalOf is why the helper did not give a file, in a few words.
func refusalOf(code workspacefs.Code, bot string) string {
	switch code {
	case workspacefs.CodeNotFound:
		return "it is not there"
	case workspacefs.CodeTooLarge:
		return fmt.Sprintf("it is larger than %d MB", workspacefs.MaxSend>>20)
	case workspacefs.CodeInvalidPath:
		return "only a file in " + bot + "'s own folder can be sent"
	default:
		return "it could not be read"
	}
}

// uploadName is a file's name as Lark is given it: control characters,
// quotes and backslashes become _, and the name stays as written otherwise,
// Vietnamese and all (OpenClaw media.ts, sanitizeFileNameForUpload).
func uploadName(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '\\' {
			return '_'
		}

		return r
	}, name)
}

// shownName is a file's name in the line that says it was not sent: one
// line, no mention tag.
func shownName(asked string) string {
	return strings.NewReplacer("<", "_", ">", "_").Replace(uploadName(path.Base(asked)))
}

// The first bytes of the files Lark files under a type of its own.
var (
	pdfMagic  = []byte("%PDF-")
	zipMagic  = []byte("PK\x03\x04")
	oleMagic  = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	fileTypes = map[string]struct {
		kind  string
		magic []byte
	}{
		".pdf": {"pdf", pdfMagic},
		".doc": {"doc", oleMagic}, ".docx": {"doc", zipMagic},
		".xls": {"xls", oleMagic}, ".xlsx": {"xls", zipMagic},
		".ppt": {"ppt", oleMagic}, ".pptx": {"ppt", zipMagic},
	}
)

// fileType is the file_type Lark files an upload under: pdf, doc, xls or ppt
// by the name's extension (OpenClaw media.ts, detectFileType), when the
// file's bytes are of that kind, and stream for anything else. A name alone
// does not make a file what it says (OpenClaw: never pass a filename to
// signature detection).
func fileType(name string, data []byte) string {
	t, ok := fileTypes[strings.ToLower(path.Ext(name))]
	if !ok || !bytes.HasPrefix(data, t.magic) {
		return "stream"
	}

	return t.kind
}

// attach sends the files a reply asked for, one after another, after its
// text. Each gets its own wait, as an upload of 30 MB takes a while.
func (r *relay) attach(ctx context.Context, paths []string) {
	for _, asked := range paths {
		r.attachOne(ctx, asked)
	}
}

func (r *relay) attachOne(ctx context.Context, asked string) {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()

	a, refusal := r.all.attachment(ctx, r.chat.Bot, asked)
	if refusal == "" {
		err := r.lark.SendFile(ctx, r.chat.LarkChatID, a)
		if err == nil {
			return
		}

		r.failed(ctx, "sending a file", err)

		refusal = "Lark did not take it"
	}

	r.all.logger.InfoContext(ctx, "a file a Bot asked to send was not sent", slog.String("event", "larkbot.file_refused"),
		slog.String("channel", r.chat.Channel), slog.String("chat_id", r.chat.ChatID), slog.String("reason", refusal))

	if err := r.lark.SendText(ctx, r.chat.LarkChatID, fmt.Sprintf(noticeNotSent, shownName(asked), refusal)); err != nil {
		r.failed(ctx, "saying a file was not sent", err)
	}
}
