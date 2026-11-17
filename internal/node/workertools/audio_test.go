package workertools

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario (issue #723, decision 1.4): a worker transcribes a recording it
// has on disk. The file is read on its machine and sent whole with its name
// and type; the answer is the transcript and who heard it. A transcript
// longer than 20,000 characters comes as its start and its end, and is saved
// whole beside the recording, whose path the answer gives.
func TestAWorkerTranscribesARecordingOnItsMachine(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 15000) + strings.Repeat("b", 15000)
	words, err := json.Marshal(long)
	require.NoError(t, err)

	cp := &controlPlane{result: `{"content":[{"type":"text","text":` + string(words) + `},` +
		`{"type":"text","text":"Heard by soniox with stt-async-v5."}],"isError":false}`}
	c := browsing(t, cp)

	work := t.TempDir()
	recording := filepath.Join(work, "meeting.mp3")
	require.NoError(t, os.WriteFile(recording, []byte("ID3 meeting"), 0o600))

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "audio_transcribe", "arguments": map[string]any{"path": recording, "language": "vi", "speakers": true},
	}})

	answer := text(t, c.read())
	saved := filepath.Join(work, "meeting.transcript.txt")

	assert.True(t, strings.HasPrefix(answer, strings.Repeat("a", 10000)+"\n\n[… 10000 characters left out …]\n\n"+
		strings.Repeat("b", 10000)+"\n\nThe whole transcript, 30000 characters, is saved in "+saved+"."), answer[:80])
	assert.True(t, strings.HasSuffix(answer, "\nHeard by soniox with stt-async-v5."))

	whole, err := os.ReadFile(saved) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, long, string(whole))

	require.Len(t, cp.calls, 1)
	assert.Equal(t, "audio_transcribe", cp.calls[0].Tool)

	var sent agenttool.TranscribeCall
	require.NoError(t, json.Unmarshal(cp.calls[0].Arguments, &sent))
	assert.Equal(t, agenttool.TranscribeCall{
		Audio: []byte("ID3 meeting"), FileName: "meeting.mp3", MIME: "audio/mpeg", Language: "vi", Speakers: true,
	}, sent)
}

// Scenario: a recording that cannot be sent says why, in words the worker
// can act on, and reaches no provider: one missing, one empty, one past the
// limit of a call.
func TestARecordingThatCannotBeSentSaysWhy(t *testing.T) {
	t.Parallel()

	cp := &controlPlane{result: `{"content":[{"type":"text","text":"x"}],"isError":false}`}
	c := browsing(t, cp)

	work := t.TempDir()
	empty := filepath.Join(work, "empty.mp3")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))

	big := filepath.Join(work, "big.wav")
	f, err := os.Create(big) //nolint:gosec // the test's own file
	require.NoError(t, err)
	require.NoError(t, f.Truncate(agenttool.MaxTranscribeBytes+1))
	require.NoError(t, f.Close())

	for i, want := range map[string]string{
		filepath.Join(work, "none.mp3"): "file not found: " + filepath.Join(work, "none.mp3"),
		empty:                           empty + " is empty",
		big:                             big + " is larger than 47 MiB, the most one call takes",
	} {
		c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
			"name": "audio_transcribe", "arguments": map[string]any{"path": i},
		}})
		assert.Equal(t, want, text(t, c.read()))
	}

	assert.Empty(t, cp.calls)
}

// Scenario (decision 1.3): a worker makes speech. The control plane's audio
// is saved where the worker asked, with the format's extension, the format
// read from the path's own when none is named; without a path it is a new
// file in the temporary folder. The answer says who spoke it and where it is.
func TestAWorkerSavesTheSpeechItMakes(t *testing.T) {
	t.Parallel()

	speech := base64.StdEncoding.EncodeToString([]byte("RIFF speech"))
	cp := &controlPlane{result: `{"content":[{"type":"text","text":"Spoken by elevenlabs with ` +
		`eleven_multilingual_v2."},{"type":"audio","data":"` + speech + `","mimeType":"audio/wav"}],"isError":false}`}
	c := browsing(t, cp)

	work := t.TempDir()

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "audio_speak", "arguments": map[string]any{"text": "Xin chào", "path": filepath.Join(work, "hi.wav")},
	}})

	want := filepath.Join(work, "hi.wav")
	assert.Equal(t, "Spoken by elevenlabs with eleven_multilingual_v2.\nSaved to "+want, text(t, c.read()))

	saved, err := os.ReadFile(want) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, "RIFF speech", string(saved))

	var sent map[string]any
	require.NoError(t, json.Unmarshal(cp.calls[0].Arguments, &sent))
	assert.Equal(t, "wav", sent["format"], "the path's extension is the format")
	assert.Equal(t, "Xin chào", sent["text"])

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "audio_speak", "arguments": map[string]any{"text": "hi"},
	}})

	answer := text(t, c.read())
	temp, ok := strings.CutPrefix(answer, "Spoken by elevenlabs with eleven_multilingual_v2.\nSaved to ")
	require.True(t, ok, answer)
	t.Cleanup(func() { _ = os.Remove(temp) })
	assert.Equal(t, os.TempDir(), filepath.Dir(temp))
	assert.Equal(t, ".mp3", filepath.Ext(temp))
}
