package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Audio from the terminal (issue #694): the owner, a Routine's script and a
// Bot turn a recording into text and text into speech with these, as they
// make an image with `hypomnema image` (image.go). A transcript is printed a
// line a segment, with its time and speaker when speakers were asked; speech
// is saved to the file named, and its path printed. The providers, their
// switches, keys and each kind's order are what Settings → Gateway → Audio
// shows and changes, as `hypomnema web providers|keys|order` does for web
// search.

func audioTranscribeFlags(fs *flag.FlagSet) {
	fs.Bool("speakers", false, "say who spoke when")
	fs.String("language", "", "the recording's language, an ISO-639-1 code such as vi (default: found)")
	fs.String("provider", "", "ask only this provider: soniox or elevenlabs (default: the first that answers)")
	jsonFlag(fs)
}

func audioSpeakFlags(fs *flag.FlagSet) {
	fs.String("out", "", "the file to save the speech in (required)")
	fs.String("o", "", "the same as --out")
	fs.String("voice", "", "a voice of the provider, or one of OpenAI's names (default: the provider's)")
	fs.String("format", "", "mp3, wav, pcm or opus (default: mp3)")
	fs.Float64("speed", 0, "the voice's speed: Soniox 0.7 to 1.3, ElevenLabs its own range (default: the provider's)")
	fs.String("language", "", "the text's language for Soniox (default: en)")
	fs.String("provider", "", "ask only this provider: soniox or elevenlabs (default: the first that answers)")
	jsonFlag(fs)
}

func audioTranscribe(ctx context.Context, e *env, fs *flag.FlagSet) error {
	path := fs.Arg(0)

	data, err := os.ReadFile(path) //nolint:gosec // the caller names the recording it wants sent
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	mime, name := http.DetectContentType(data), filepath.Base(path)
	speakers := boolean(fs, "speakers")
	body := openapi.TranscribeRequest{
		Audio: data, FileName: &name, MimeType: &mime, Language: optional(fs, "language"),
		Speakers: &speakers,
	}

	if p := str(fs, "provider"); p != "" {
		provider := openapi.AudioProvider(p)
		body.Provider = &provider
	}

	resp, err := e.api.TranscribeAudioWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("transcribing %s: %w", path, err)
	}

	if resp.JSON200 == nil {
		return apiError("transcribing "+path, resp.StatusCode(), resp.JSONDefault)
	}

	t := resp.JSON200

	return e.print(fs, t, func() {
		if speakers {
			for _, s := range t.Segments {
				e.printf("[%s] %s: %s\n", clock(s.Start), speakerName(s.Speaker), s.Text)
			}
		} else {
			e.printf("%s\n", t.Text)
		}

		e.printf("%s.\n", answeredText("Heard by", string(t.Provider), t.Model, t.Errors))
	})
}

// clock is seconds from the start as m:ss, or h:mm:ss past an hour.
func clock(seconds float32) string {
	d := time.Duration(float64(seconds) * float64(time.Second)).Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60

	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}

	return fmt.Sprintf("%d:%02d", m, s)
}

func speakerName(s *string) string {
	if s == nil || *s == "" {
		return "speaker"
	}

	if _, err := strconv.Atoi(*s); err == nil {
		return "speaker " + *s
	}

	return *s
}

// answeredText says who answered, and why those before did not.
func answeredText(verb, provider, model string, failures *[]openapi.WebSearchError) string {
	line := fmt.Sprintf("%s %s with %s", verb, provider, model)

	for i, f := range val(failures) {
		sep := ", after "
		if i > 0 {
			sep = "; "
		}

		line += sep + f.Provider + " failed (" + f.Message + ")"
	}

	return line
}

func audioSpeak(ctx context.Context, e *env, fs *flag.FlagSet) error {
	out := str(fs, "out")
	if out == "" {
		out = str(fs, "o")
	}

	if out == "" {
		return fmt.Errorf("%w: audio speak <text> -o <file>", ErrUsage)
	}

	body := openapi.SpeakRequest{
		Input: strings.Join(fs.Args(), " "), Voice: optional(fs, "voice"), Language: optional(fs, "language"),
	}

	if f := str(fs, "format"); f != "" {
		format := openapi.SpeakRequestFormat(f)
		body.Format = &format
	} else if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(out)), "."); ext != "" {
		// The file's extension is the format when it is one.
		switch format := openapi.SpeakRequestFormat(ext); format {
		case openapi.Mp3, openapi.Wav, openapi.Pcm, openapi.Opus:
			body.Format = &format
		}
	}

	if speed, _ := strconv.ParseFloat(str(fs, "speed"), 64); speed > 0 {
		s := float32(speed)
		body.Speed = &s
	}

	if p := str(fs, "provider"); p != "" {
		provider := openapi.AudioProvider(p)
		body.Provider = &provider
	}

	resp, err := e.api.SpeakAudioWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("making the speech: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("making the speech", resp.StatusCode(), resp.JSONDefault)
	}

	a := resp.JSON200

	path, err := filepath.Abs(out)
	if err != nil {
		return err
	}

	if len(a.Audio) == 0 {
		return errors.New("the answer held no audio")
	}

	if err := os.WriteFile(path, a.Audio, 0o600); err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}

	saved := struct {
		Provider   openapi.AudioProvider    `json:"provider"`
		Model      string                   `json:"model"`
		Format     string                   `json:"format"`
		MimeType   string                   `json:"mime_type"`
		Characters int                      `json:"characters"`
		Path       string                   `json:"path"`
		Errors     []openapi.WebSearchError `json:"errors,omitempty"`
	}{a.Provider, a.Model, a.Format, a.MimeType, a.Characters, path, val(a.Errors)}

	return e.print(fs, saved, func() {
		e.printf("%s\n", path)
		e.printf("%s.\n", answeredText("Spoken by", string(a.Provider), a.Model, a.Errors))
	})
}

func audioProviders(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetAudioWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the audio providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the audio providers", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printAudio(resp.JSON200) })
}

// printAudio prints a line a provider with its state, its keys after it a line
// each; then each kind's order, a line a provider with its model and usage.
func (e *env) printAudio(a *openapi.Audio) {
	for _, p := range a.Providers {
		state := strings.ReplaceAll(string(p.State), "_", " ")
		if p.State == openapi.AudioProviderLinkStateResting {
			state = restText(p.Rest, p.RestingUntil)
		}

		e.printf("%s\t%s\n", p.Provider, state)

		if p.Voice != nil {
			e.printf("\tvoice for videos %s\n", *p.Voice)
		}

		for _, k := range p.Keys {
			state := string(k.State)
			if k.State == openapi.AudioKeyStateResting {
				state = restText(k.Rest, k.RestingUntil)
			}

			e.printf("\tkey %s\t%s\n", k.Name, state)
		}
	}

	for _, kind := range []struct {
		name  string
		links []openapi.AudioKindLink
	}{{"speech-to-text", a.Stt}, {"text-to-speech", a.Tts}} {
		e.printf("%s:\n", kind.name)

		for i, l := range kind.links {
			amount := fmt.Sprintf("%.0f s heard", l.Usage.Month.Seconds)
			if kind.name == "text-to-speech" {
				amount = fmt.Sprintf("%d characters spoken", l.Usage.Month.Characters)
			}

			e.printf("%d\t%s\t%s\ttoday %d answered, %d failed; this month %d answered, %d failed, %s\n", i+1,
				l.Provider, l.Model, l.Usage.Today.Answered, l.Usage.Today.Failed, l.Usage.Month.Answered,
				l.Usage.Month.Failed, amount)
		}
	}
}

func audioProvidersSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, state := fs.Arg(0), fs.Arg(1)
	if state != "on" && state != "off" {
		return fmt.Errorf("%w: audio providers set <provider> on|off", ErrUsage)
	}

	resp, err := e.api.SetAudioProviderWithResponse(ctx, openapi.AudioProvider(provider),
		openapi.AudioProviderChange{On: state == "on"})
	if err != nil {
		return fmt.Errorf("switching %s: %w", provider, err)
	}

	if resp.JSON200 == nil {
		return apiError("switching "+provider, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("audio provider %s is %s\n", provider, state) })
}

func audioOrder(ctx context.Context, e *env, fs *flag.FlagSet) error {
	kind := fs.Arg(0)
	if kind != "stt" && kind != "tts" {
		return fmt.Errorf("%w: audio order stt|tts <provider> <provider>", ErrUsage)
	}

	var body openapi.AudioOrder
	for _, p := range fs.Args()[1:] {
		body.Providers = append(body.Providers, openapi.AudioProvider(p))
	}

	resp, err := e.api.SetAudioOrderWithResponse(ctx, openapi.AudioKind(kind), body)
	if err != nil {
		return fmt.Errorf("ordering the audio providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("ordering the audio providers", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printAudio(resp.JSON200) })
}

func audioKeysAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider := fs.Arg(0)

	file := str(fs, "key-file")
	if file == "" {
		return errors.New("pass --key-file with the file holding the key, or - to read it from standard input")
	}

	key, err := e.read(file)
	if err != nil {
		return err
	}

	key = strings.TrimSpace(key)

	resp, err := e.api.AddAudioKeyWithResponse(ctx, openapi.AudioProvider(provider), openapi.AudioNewKey{Key: &key})
	if err != nil {
		return fmt.Errorf("keeping %s's key: %w", provider, err)
	}

	if resp.JSON201 == nil {
		return apiError("keeping "+provider+"'s key", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON201, func() {
		e.printf("%s's key is kept in the vault as %s\n", provider, resp.JSON201.Name)
	})
}

func audioKeysRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, name := fs.Arg(0), fs.Arg(1)

	resp, err := e.api.RemoveAudioKeyWithResponse(ctx, openapi.AudioProvider(provider), name)
	if err != nil {
		return fmt.Errorf("removing %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("removing "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s was removed from the vault\n", name) })
}
