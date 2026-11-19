package agenttool

// ToolWhatsAppSend sends a message or a file in one of the owner's WhatsApp chats as him, after his approval (issue
// #645). The file is named by a path in the folder the agent works in, which its tools server reads into File, as
// show_artifact's files are read (internal/shared/toolsmcp/whatsapp.go).
const ToolWhatsAppSend = "whatsapp_send"

const HeaderWhatsAppMentions = "Hypomnema-WhatsApp-Mentions"

// WhatsAppSendCall is the arguments of a whatsapp_send or zalo_send call as the control plane takes them: the
// agent's own, and the file its tools server read from Path. A server of an earlier release passes the agent's
// arguments on unchanged, without File.
type WhatsAppSendCall struct {
	Account  string `json:"account,omitempty"`
	Chat     string `json:"chat"`
	Text     string `json:"text,omitempty"`
	Path     string `json:"path,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty"`
	File     []byte `json:"file,omitempty"`
	FileName string `json:"file_name,omitempty"`
	// Mentions are the members a whatsapp_send tags, by their ids as whatsapp_messages gives a sender (issue #852).
	Mentions []string `json:"mentions,omitempty"`
}

// SendsAFile says a tool sends a file of the folder the agent works in, which its tools server reads into
// WhatsAppSendCall.File: whatsapp_send and zalo_send.
func SendsAFile(tool string) bool { return tool == ToolWhatsAppSend || tool == ToolZaloSend }
