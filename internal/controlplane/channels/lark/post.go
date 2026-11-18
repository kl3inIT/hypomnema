package lark

import (
	"encoding/json"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/channels"
)

// A rich-text post (issue #829) is read as OpenClaw's parsePostContent
// (extensions/feishu/src/post.ts) and Hermes's parse_feishu_post_payload
// (plugins/platforms/feishu/adapter.py) read it: its title, then each row of
// elements as a line of text; an img is an image to download, a media a
// file, and so is each of the post's top-level files. A mention stays the
// placeholder Lark writes for it (@_user_1), which the message's mentions
// name, as in a text message: the Channels take the bot's own out and name
// the others.

// postElement is one element of a post's row: its tag and the fields the
// tags use.
type postElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Content  string `json:"content"`
	Href     string `json:"href"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	ImageKey string `json:"image_key"`
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
	Emoji    string `json:"emoji_type"`
	Language string `json:"language"`
}

// postFile is one of a post's top-level files.
type postFile struct {
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
	IsFolder bool   `json:"is_folder"`
}

// postBody is a post's own content: a title, rows of elements and files.
type postBody struct {
	Title   string          `json:"title"`
	Content [][]postElement `json:"content"`
	Files   []postFile      `json:"files"`
}

// postOf is the text of a post's content, which is JSON, and the files in
// it, each once. The content is the post itself, as a received message's is,
// or a post by language, as one Lark sends is (Hermes _resolve_post_payload).
func postOf(content string) (string, []channels.File) {
	body, ok := postBodyOf(content)
	if !ok {
		return "", nil
	}

	var (
		lines []string
		files []channels.File
	)

	seen := map[string]bool{}
	add := func(f channels.File) {
		if f.Key != "" && !seen[f.Key] {
			seen[f.Key] = true
			files = append(files, f)
		}
	}

	if title := strings.TrimSpace(body.Title); title != "" {
		lines = append(lines, title)
	}

	for _, row := range body.Content {
		var b strings.Builder

		for _, e := range row {
			b.WriteString(elementText(e, add))
		}

		if line := strings.TrimSpace(b.String()); line != "" {
			lines = append(lines, line)
		}
	}

	for _, f := range body.Files {
		if !f.IsFolder {
			add(channels.File{Key: f.FileKey, Name: f.FileName})
		}
	}

	return strings.Join(lines, "\n"), files
}

// postBodyOf reads a post's content: the post itself, or, by language, the
// first that has rows.
func postBodyOf(content string) (postBody, bool) {
	var direct struct {
		postBody

		Post map[string]postBody `json:"post"`
	}

	if json.Unmarshal([]byte(content), &direct) == nil && direct.Content != nil {
		return direct.postBody, true
	}

	byLanguage := direct.Post
	if byLanguage == nil {
		_ = json.Unmarshal([]byte(content), &byLanguage)
	}

	for _, language := range []string{"zh_cn", "en_us", "ja_jp"} {
		if body, ok := byLanguage[language]; ok && body.Content != nil {
			return body, true
		}
	}

	for _, body := range byLanguage {
		if body.Content != nil {
			return body, true
		}
	}

	return postBody{}, false
}

// elementText is the text of one element of a post; an image or a file is
// given to add and has none.
func elementText(e postElement, add func(channels.File)) string {
	text := e.Text
	if text == "" {
		text = e.Content
	}

	switch strings.ToLower(e.Tag) {
	case "a":
		if e.Href == "" {
			return text
		}

		if text == "" {
			return e.Href
		}

		return "[" + text + "](" + e.Href + ")"
	case "at":
		// The placeholder the message's mentions name (Hermes: user_id is "@_user_N" or "@_all").
		if strings.HasPrefix(e.UserID, "@_") {
			return e.UserID
		}

		if e.UserName != "" {
			return "@" + e.UserName
		}

		return ""
	case "img", "image":
		add(channels.File{Key: e.ImageKey, Image: true})

		return ""
	case "media", "file", "video":
		add(channels.File{Key: e.FileKey, Name: e.FileName})

		return ""
	case "emotion", "emoji":
		if text != "" {
			return text
		}

		if e.Emoji != "" {
			return ":" + e.Emoji + ":"
		}

		return ""
	case "br":
		return "\n"
	case "hr", "divider":
		return "\n---\n"
	case "code":
		if text == "" {
			return ""
		}

		return "`" + text + "`"
	case "code_block", "pre":
		return "\n```" + e.Language + "\n" + strings.TrimSuffix(text, "\n") + "\n```\n"
	default:
		// text, md, lark_md and any tag Lark adds later: its text.
		return text
	}
}

// fileOf is the file an image, a file or a video message is: its content,
// which is JSON, names it by image_key or file_key, and a file by file_name
// too (OpenClaw parseMediaResource).
func fileOf(kind, content string) (channels.File, bool) {
	var parsed struct {
		ImageKey string `json:"image_key"`
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}

	if json.Unmarshal([]byte(content), &parsed) != nil {
		return channels.File{}, false
	}

	if kind == "image" {
		return channels.File{Key: parsed.ImageKey, Image: true}, parsed.ImageKey != ""
	}

	return channels.File{Key: parsed.FileKey, Name: parsed.FileName}, parsed.FileKey != ""
}
