package toolsmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// WhatsAppSendCall reads the file whatsapp_send names, in dir (the folder the agent works in), by the rules
// show_artifact reads one file by (artifactpack.Read: inside dir, at most artifactpack.MaxTotal), and sends it with
// the call on path (PathToolsCall), as ArtifactCall does (issue #645).
func (a *Agent) WhatsAppSendCall(ctx context.Context, path string, arguments json.RawMessage, dir string) (any, error) {
	var in agenttool.WhatsAppSendCall
	if len(arguments) > 0 && json.Unmarshal(arguments, &in) != nil {
		return nil, errors.New("whatsapp_send takes {chat, text, path, reply_to}")
	}

	// The file is this server's to give, never the agent's.
	in.File, in.FileName = nil, ""

	if in.Path != "" {
		files, _, err := artifactpack.Read(dir, in.Path)
		if err != nil {
			return nil, err
		}

		if len(files) != 1 {
			return nil, errors.New("whatsapp_send sends one file: name a file, not a folder")
		}

		in.File, in.FileName = files[0].Data, files[0].Path
	}

	args, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	// Sending waits for the owner's approval as long as a held request does.
	return a.ToolCall(ctx, path, CallTimeout, agenttool.ToolWhatsAppSend, args)
}
