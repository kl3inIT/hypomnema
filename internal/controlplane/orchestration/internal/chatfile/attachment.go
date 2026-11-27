// Package chatfile defines files uploaded for a message or an Inbox wake.
package chatfile

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// Attachment is a file the owner attached to a message (issue #343), as t3code's chat attachment (name, size,
// type) and OpenMausBot's file by path: the file was uploaded into the chat's Bot's folder, and pi reads it there.
type Attachment struct {
	// Name is the file's name as the owner picked it.
	Name string `json:"name"`
	// Path is where it is in the Bot's folder: attachments/<chat id>/<file>.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Type is its media type as the browser named it; empty when it named none.
	Type string `json:"type,omitempty"`
}

// Dir is the folder of a chat's attachments in its Bot's folder (issue #343), kept apart from the work
// as Orca's agent-session-attachments and OpenMausBot's DATA_DIR/attachments are.
func Dir(chatID string) string { return "attachments/" + chatID }

// maxAttachmentField bounds an attachment's name and type.
const maxAttachmentField = 255

// Check validates the metadata and requires the path to be in this chat’s attachments directory.
func (a Attachment) Check(chatID string) error {
	dir, file := path.Split(a.Path)

	switch {
	case dir != Dir(chatID)+"/" || file == "" || file == "." || file == ".." || !plainLine(file):
		return fmt.Errorf("%w: an attachment is a file of %s, not %q", storeerr.ErrInvalid, Dir(chatID), a.Path)
	case strings.TrimSpace(a.Name) == "":
		return fmt.Errorf("%w: an attachment needs its name", storeerr.ErrInvalid)
	case len(a.Name) > maxAttachmentField || len(a.Type) > maxAttachmentField:
		return fmt.Errorf("%w: an attachment's name and type are at most %d bytes", storeerr.ErrInvalid, maxAttachmentField)
	case !plainLine(a.Name) || !plainLine(a.Type):
		return fmt.Errorf("%w: an attachment's name and type are one line of text", storeerr.ErrInvalid)
	case a.Size < 0 || a.Size > workspacefs.MaxUpload:
		return fmt.Errorf("%w: an attachment is at most %d MiB", storeerr.ErrInvalid, workspacefs.MaxUpload>>20)
	}

	return nil
}

// plainLine says whether s is UTF-8 without a control character, so that a name is one line of what pi is given.
func plainLine(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}

	return true
}
