package agenttool

// The arguments of audio_transcribe as the control plane takes them (issue
// #723, decision 1.4): the tools server on the agent's machine reads the
// recording the agent names and sends it whole, as it sends generate_image's
// images (ImageCall).
type TranscribeCall struct {
	Audio    []byte   `json:"audio"`
	FileName string   `json:"file_name,omitempty"`
	MIME     string   `json:"mime_type,omitempty"`
	Language string   `json:"language,omitempty"`
	Speakers bool     `json:"speakers,omitempty"`
	Terms    []string `json:"terms,omitempty"`
}

const (
	// MaxTranscribeBytes bounds the recording one audio_transcribe sends: it
	// fits in one call (MaxCallBytes) once encoded in base64, with room for
	// the call's JSON. The design's 48 MiB (decision 1.4, itself inferred
	// from MaxCallBytes) is exactly 64 MiB once encoded and leaves none, so
	// it is 47. A larger one goes to `hypomnema audio transcribe` or a
	// Meeting.
	MaxTranscribeBytes = MaxCallBytes/4*3 - 1<<20
	// MaxTranscriptChars is the most of a transcript a tool's answer holds:
	// a longer one is given as its start and its end, and saved whole in a
	// file whose path is given, as Hermes's web_extract cuts a long page
	// (tools/web_tools.py:543) and web_fetch's 20,000 characters.
	MaxTranscriptChars = 20000
)
