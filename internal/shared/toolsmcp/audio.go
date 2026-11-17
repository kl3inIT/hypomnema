package toolsmcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The speech tools (issue #723, decisions 1.3 and 1.4) are the control
// plane's, as generate_image is, and their files are the agent's machine's
// as its images are (image.go): audio_transcribe's recording is read here
// and sent whole, and audio_speak's speech is saved here and its path given,
// with the MEDIA line that sends it into a Bot's chat, as Hermes's
// text_to_speech answers a MEDIA path (tools/tts_tool.py:655-685).
const (
	// maxAudioAnswerBytes bounds an answer with speech or a transcript: 4,096
	// characters spoken, or five hours heard, in JSON.
	maxAudioAnswerBytes = 64 << 20
)

// audioFormats are the formats speech is made in, with their files'
// extensions (speech.Formats).
var audioFormats = map[string]string{"mp3": ".mp3", "wav": ".wav", "pcm": ".pcm", "opus": ".opus"}

// Audio says where the speech tools ask and save.
type Audio struct {
	// Path is the agent API's route of the call, PathToolsCall.
	Path string
	// Folder, when set, is where speech is saved when no path is asked, and
	// the answer gives the MEDIA line that sends it into the chat: a Bot's
	// pi, as Images.Folder. Empty: a new file in the temporary folder.
	Folder string
}

// TranscribeCall reads the recording named, asks the control plane to hear
// it and answers with what was said. A transcript longer than the answer
// holds is given as its start and its end, and saved whole next to the
// recording.
func (a *Agent) TranscribeCall(ctx context.Context, arguments json.RawMessage, where Audio) (any, error) {
	var in struct {
		Path     string   `json:"path"`
		Language string   `json:"language"`
		Speakers bool     `json:"speakers"`
		Terms    []string `json:"terms"`
	}

	if len(arguments) > 0 && json.Unmarshal(arguments, &in) != nil {
		return nil, errors.New("audio_transcribe takes {path, language, speakers, terms}")
	}

	if strings.TrimSpace(in.Path) == "" {
		return nil, errors.New("name the recording's path")
	}

	data, err := readRecording(in.Path)
	if err != nil {
		return nil, err
	}

	args, err := json.Marshal(agenttool.TranscribeCall{
		Audio: data, FileName: filepath.Base(in.Path), MIME: http.DetectContentType(data), Language: in.Language,
		Speakers: in.Speakers, Terms: in.Terms,
	})
	if err != nil {
		return nil, err
	}

	raw, err := a.toolCall(ctx, where.Path, ImageCallTimeout, maxAudioAnswerBytes, agenttool.ToolAudioTranscribe, args)
	if err != nil {
		return nil, err
	}

	result, err := readResult(raw)
	if err != nil || result.IsError {
		return raw, err
	}

	var texts []string

	for _, c := range result.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}

	if len(texts) == 0 {
		return nil, errors.New("the control plane answered without a transcript")
	}

	text, err := clipTranscript(texts[0], in.Path)
	if err != nil {
		return nil, err
	}

	return Text(strings.Join(append([]string{text}, texts[1:]...), "\n"), false), nil
}

// SpeakCall asks the control plane for the speech and saves it.
func (a *Agent) SpeakCall(ctx context.Context, arguments json.RawMessage, where Audio) (any, error) {
	var in struct {
		Text     string  `json:"text"`
		Voice    string  `json:"voice"`
		Format   string  `json:"format"`
		Speed    float64 `json:"speed"`
		Language string  `json:"language"`
		Path     string  `json:"path"`
	}

	if len(arguments) > 0 && json.Unmarshal(arguments, &in) != nil {
		return nil, errors.New("audio_speak takes {text, voice, format, speed, language, path}")
	}

	format := strings.ToLower(strings.TrimSpace(in.Format))
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(in.Path)), "."); format == "" && audioFormats[ext] != "" {
		// The file's extension is the format when it is one, as `hypomnema audio speak` takes it.
		format = ext
	}

	if format == "" {
		format = "mp3"
	}

	args, err := json.Marshal(map[string]any{
		"text": in.Text, "voice": in.Voice, "format": format, "speed": in.Speed, "language": in.Language,
	})
	if err != nil {
		return nil, err
	}

	raw, err := a.toolCall(ctx, where.Path, ImageCallTimeout, maxAudioAnswerBytes, agenttool.ToolAudioSpeak, args)
	if err != nil {
		return nil, err
	}

	result, err := readResult(raw)
	if err != nil || result.IsError {
		return raw, err
	}

	var (
		lines []string
		saved bool
	)

	for _, c := range result.Content {
		switch c.Type {
		case "text":
			lines = append(lines, c.Text)
		case "audio":
			if saved {
				continue
			}

			path, err := saveSpeech(in.Path, where.Folder, format, c.Data)
			if err != nil {
				return nil, err
			}

			saved = true

			lines = append(lines, "Saved to "+path)
			if where.Folder != "" {
				lines = append(lines, mediaLine(path))
			}
		}
	}

	if !saved {
		return nil, errors.New("the control plane answered without speech")
	}

	return Text(strings.Join(lines, "\n"), false), nil
}

type toolResult struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func readResult(raw json.RawMessage) (toolResult, error) {
	var result toolResult
	if json.Unmarshal(raw, &result) != nil {
		return toolResult{}, errors.New("the control plane's answer could not be read")
	}

	return result, nil
}

// readRecording reads a recording to send, with readImage's checks.
func readRecording(name string) ([]byte, error) {
	info, err := os.Stat(name)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("file not found: %s", name)
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", name, err)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a file", name)
	case info.Size() == 0:
		return nil, fmt.Errorf("%s is empty", name)
	case info.Size() > agenttool.MaxTranscribeBytes:
		return nil, fmt.Errorf("%s is larger than %d MiB, the most one call takes", name,
			agenttool.MaxTranscribeBytes>>20)
	}

	data, err := os.ReadFile(name) //nolint:gosec // the agent names the recording it wants heard
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}

	return data, nil
}

// clipTranscript is a transcript as the answer holds it: whole when it is
// short; else its start and its end, the whole saved in a file next to the
// recording (or in the temporary folder when that one cannot be written),
// whose path is given.
func clipTranscript(text, recording string) (string, error) {
	if utf8.RuneCountInString(text) <= agenttool.MaxTranscriptChars {
		return text, nil
	}

	path, err := saveTranscript(text, recording)
	if err != nil {
		return "", err
	}

	runes := []rune(text)
	half := agenttool.MaxTranscriptChars / 2

	return fmt.Sprintf("%s\n\n[… %d characters left out …]\n\n%s\n\nThe whole transcript, %d characters, is saved "+
		"in %s.", string(runes[:half]), len(runes)-2*half, string(runes[len(runes)-half:]), len(runes), path), nil
}

func saveTranscript(text, recording string) (string, error) {
	path, err := filepath.Abs(strings.TrimSuffix(recording, filepath.Ext(recording)) + ".transcript.txt")
	if err == nil {
		if err = os.WriteFile(path, []byte(text), 0o600); err == nil {
			return path, nil
		}
	}

	f, err := os.CreateTemp("", "hypomnema-transcript-*.txt")
	if err != nil {
		return "", fmt.Errorf("making the transcript's file: %w", err)
	}

	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return "", fmt.Errorf("writing %s: %w", f.Name(), err)
	}

	return filepath.Abs(f.Name())
}

// saveSpeech writes the speech and returns its absolute path: at the path
// asked, its extension the format's, or a new file in folder, the temporary
// folder when it is empty.
func saveSpeech(asked, folder, format, b64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("the speech is not base64: %w", err)
	}

	ext := audioFormats[format]
	if ext == "" {
		ext = ".mp3"
	}

	if asked == "" {
		prefix := "hypomnema-speech-*"
		if folder != "" {
			prefix = "speech-*"

			if err := os.MkdirAll(folder, 0o750); err != nil {
				return "", fmt.Errorf("making %s: %w", folder, err)
			}
		}

		f, err := os.CreateTemp(folder, prefix+ext)
		if err != nil {
			return "", fmt.Errorf("making the speech's file: %w", err)
		}

		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}

		if err != nil {
			return "", fmt.Errorf("writing %s: %w", f.Name(), err)
		}

		return filepath.Abs(f.Name())
	}

	path, err := filepath.Abs(asked)
	if err != nil {
		return "", fmt.Errorf("the path %s: %w", asked, err)
	}

	path = strings.TrimSuffix(path, filepath.Ext(path)) + ext

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}

	return path, nil
}
