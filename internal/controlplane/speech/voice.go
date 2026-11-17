package speech

import "strings"

// What an agent reads of a voice message a person sent (issue #723, incoming
// voice): a Lark direct message (controlplane/channels) and a WhatsApp or
// Zalo voice note a Bot opens (agentapi, messaging_tools.go) are transcribed
// before the model reads them, as Hermes (gateway/config.py:635) and OpenClaw
// (src/media-understanding/audio-preflight.ts) transcribe one before the
// turn. The words are Hermes's: the transcript as a plain quoted line, since
// a "The user sent a voice message" wrapper read as a meta-instruction
// (gateway/run_inbound_transcription.py:142-151); one neutral line for a
// failure, with no word of setting a provider up (:340-342); and a line for
// a recording with no words in it, so that the model asks rather than
// guesses (:363-371).
const (
	VoiceUntranscribed = "[voice message could not be transcribed]"
	VoiceEmpty         = "[voice message came through empty or inaudible: no words were heard. Do not guess " +
		"what it said; ask for it to be sent again or typed.]"
	voiceMarker = "[voice message, transcribed]"
)

// VoiceNote is what the model reads of a voice message: its transcript, or
// why there is none.
func VoiceNote(t Transcript, err error) string {
	text := strings.TrimSpace(t.Text)

	switch {
	case err != nil:
		return VoiceUntranscribed
	case text == "":
		return VoiceEmpty
	}

	return `"` + text + `"` + "\n" + voiceMarker
}
