package clientcli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The videos made of takes (issue #818, row 11), the owner's only: listed,
// read, made, approved, asked for changes, deleted and downloaded.

func videosList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListVideosWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing videos: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing videos", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no videos\n")
		}

		for _, v := range resp.JSON200.Data {
			e.printf("%s\n", videoLine(v))
		}
	})
}

// videoLine is a video in a line: id, its latest revision and state, the approved one, its take, title.
func videoLine(v openapi.Video) string {
	last := v.Revisions[len(v.Revisions)-1]

	approved := ""
	if v.Approved != nil {
		approved = fmt.Sprintf("  approved r%d", *v.Approved)
	}

	return fmt.Sprintf("%s  r%d %s%s  %s  %s  %s", v.Id, last.Rev, revState(last), approved, v.Take,
		v.CreatedAt.Local().Format("2006-01-02 15:04"), v.Title)
}

func revState(r openapi.VideoRevision) string {
	state := string(r.State)

	switch {
	case r.State == openapi.VideoRevisionStateFailed:
		state += " at " + val(r.FailedStage) + " (" + val(r.FailedCause) + ")"
	case r.LengthMs > 0:
		state += " " + clockOf(r.LengthMs)
	}

	return state
}

func videosGet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.GetVideoWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("reading video %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("reading video "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printVideo(resp.JSON200) })
}

func (e *env) printVideo(v *openapi.Video) {
	e.printf("%s\n", videoLine(*v))

	for _, r := range v.Revisions {
		by := ""
		if r.Provider != nil {
			by = "  " + *r.Provider + " " + val(r.UsedVoice)
		}

		captions := "captions"
		if !r.Captions {
			captions = "no captions"
		}

		e.printf("  r%d %s  %s %s  %s%s\n", r.Rev, revState(r), r.Aspect, captions, lookLine(r.Look), by)

		if r.Note != nil {
			e.printf("    note: %s\n", *r.Note)
		}

		for _, w := range r.Warnings {
			e.printf("    warning: %s\n", w)
		}
	}

	last := v.Revisions[len(v.Revisions)-1]
	for i, l := range last.Lines {
		e.printf("  %d. %s\n", i+1, l)
	}
}

func videosMake(ctx context.Context, e *env, fs *flag.FlagSet) error {
	body := openapi.VideoMake{Take: fs.Arg(0), Title: optional(fs, "title"), Voice: optional(fs, "voice")}

	if a := str(fs, "aspect"); a != "" {
		aspect := openapi.VideoMakeAspect(a)
		body.Aspect = &aspect
	}

	if str(fs, "no-captions") == "true" {
		off := false
		body.Captions = &off
	}

	look, err := lookArg(fs)
	if err != nil {
		return err
	}

	body.Look = look

	resp, err := e.api.MakeVideoWithResponse(ctx, &openapi.MakeVideoParams{IdempotencyKey: key(fs)}, body)
	if err != nil {
		return fmt.Errorf("making a video of %s: %w", body.Take, err)
	}

	if resp.JSON201 == nil {
		return apiError("making a video of "+body.Take, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON201, func() { e.printVideo(resp.JSON201) })
}

// lookFlags are the flags of a look (issue #908, row 16): only the sections whose flags are given are sent.
func lookFlags(fs *flag.FlagSet) {
	fs.String("caption-preset", "", "the captions' preset, from `videos looks` (default clean)")
	fs.String("caption-position", "", "where captions sit: preset, bottom, lower_third, center or top (default preset)")
	fs.String("chrome", "", "what is drawn around the picture: full, none, android or iphone (default none)")
	fs.Int("padding", 48, "the room around the frame, 0-400")
	fs.Int("radius", 0, "the picture's corner radius, 0-80 (default 40 for a device, 14 otherwise)")
	fs.Bool("shadow", true, "the frame's shadow; -shadow=false for none")
	fs.Bool("safe-top", false, "a status strip of bezel above the picture, for a device")
	fs.String("background", "", "the background, from `videos looks` (default midnight)")
	fs.String("background-colors", "", "two colors as #rrggbb,#rrggbb, in place of a background")
	cardFlags(fs)
}

// cardFlags are the flags of a look's cards (issue #1230, row 1): the cards section is sent when one is given, with
// each card whose title is given; -no-cards sends it with none, which takes the cards away.
func cardFlags(fs *flag.FlagSet) {
	for _, c := range []struct{ name, ms string }{{"title", "2600"}, {"end", "3000"}} {
		fs.String(c.name+"-card", "", "the "+c.name+" card's title; the card is drawn on the background")
		fs.String(c.name+"-card-subtitle", "", "a line under the "+c.name+" card's title")
		fs.String(c.name+"-card-url", "", "a URL on the "+c.name+" card, in the accent")
		fs.Int(c.name+"-card-ms", 0, "how long the "+c.name+" card lasts, 500-10000 ms (default "+c.ms+")")
	}

	fs.String("card-accent", "", "the cards' accent as #rrggbb (default #6c8cff)")
	fs.Bool("no-cards", false, "no title or end card")
}

// cardsArg is the cards section the flags give, nil when no card flag is.
func cardsArg(fs *flag.FlagSet, set map[string]bool) (*openapi.VideoCardsInput, error) {
	var (
		cards openapi.VideoCardsInput
		given bool
	)

	for _, side := range []struct {
		name string
		to   **openapi.VideoCardInput
	}{{"title", &cards.Title}, {"end", &cards.End}} {
		n := side.name + "-card"
		for _, f := range []string{n, n + "-subtitle", n + "-url", n + "-ms"} {
			given = given || set[f]
		}

		if !set[n] {
			if set[n+"-subtitle"] || set[n+"-url"] || set[n+"-ms"] {
				return nil, fmt.Errorf("%w: the %s card's flags need -%s, its title", ErrUsage, side.name, n)
			}

			continue
		}

		card := &openapi.VideoCardInput{Title: str(fs, n)}
		if set[n+"-subtitle"] {
			card.Subtitle = ref(str(fs, n+"-subtitle"))
		}

		if set[n+"-url"] {
			card.CtaUrl = ref(str(fs, n+"-url"))
		}

		if set[n+"-ms"] {
			ms, err := strconv.ParseInt(str(fs, n+"-ms"), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: -%s-ms is a number of ms", ErrUsage, n)
			}

			card.DurationMs = &ms
		}

		*side.to = card
	}

	if set["card-accent"] {
		given = true
		cards.Accent = ref(str(fs, "card-accent"))
	}

	switch {
	case set["no-cards"] && given:
		return nil, fmt.Errorf("%w: -no-cards takes no other card flag", ErrUsage)
	case set["no-cards"]:
		return &openapi.VideoCardsInput{}, nil
	case given:
		return &cards, nil
	}

	return nil, nil //nolint:nilnil // No flag is no cards, and no error.
}

// lookArg is the look the flags give: a section only when one of its flags is given, a field only when its flag
// is; nil when none is.
func lookArg(fs *flag.FlagSet) (*openapi.VideoLookInput, error) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var look openapi.VideoLookInput

	if set["caption-preset"] || set["caption-position"] {
		look.Captions = &openapi.VideoCaptionsInput{}
		if set["caption-preset"] {
			look.Captions.Preset = ref(str(fs, "caption-preset"))
		}

		if set["caption-position"] {
			look.Captions.Position = ref(openapi.VideoCaptionPosition(str(fs, "caption-position")))
		}
	}

	if set["chrome"] || set["padding"] || set["radius"] || set["shadow"] || set["safe-top"] {
		look.Frame = &openapi.VideoFrameInput{}
		if set["chrome"] {
			look.Frame.Chrome = ref(openapi.VideoChrome(str(fs, "chrome")))
		}

		for name, to := range map[string]**int{"padding": &look.Frame.Padding, "radius": &look.Frame.Radius} {
			if set[name] {
				n, err := strconv.Atoi(str(fs, name))
				if err != nil {
					return nil, fmt.Errorf("%w: --%s is a number", ErrUsage, name)
				}

				*to = &n
			}
		}

		if set["shadow"] {
			look.Frame.Shadow = ref(str(fs, "shadow") == "true")
		}

		if set["safe-top"] {
			look.Frame.SafeTop = ref(str(fs, "safe-top") == "true")
		}
	}

	if set["background"] || set["background-colors"] {
		look.Background = &openapi.VideoBackground{}
		if set["background"] {
			look.Background.Preset = ref(str(fs, "background"))
		}

		if set["background-colors"] {
			colors := strings.Split(str(fs, "background-colors"), ",")
			for i := range colors {
				colors[i] = strings.TrimSpace(colors[i])
			}

			look.Background.Colors = &colors
		}
	}

	if set["music-gain"] && !set["music"] {
		return nil, fmt.Errorf("%w: -music-gain needs -music", ErrUsage)
	}

	if set["music"] {
		look.Music = &openapi.VideoMusicInput{Track: str(fs, "music")}
		if set["music-gain"] {
			gain, err := strconv.ParseFloat(str(fs, "music-gain"), 64)
			if err != nil {
				return nil, fmt.Errorf("%w: -music-gain is a number of dB", ErrUsage)
			}

			look.Music.GainDb = &gain
		}
	}

	cards, err := cardsArg(fs, set)
	if err != nil {
		return nil, err
	}

	look.Cards = cards

	if look.Captions == nil && look.Frame == nil && look.Background == nil && look.Music == nil && look.Cards == nil {
		return nil, nil //nolint:nilnil // No flag is no look, and no error.
	}

	return &look, nil
}

// musicFlags are the flags of a look's music (issue #910): sent together, as the music section needs its track.
func musicFlags(fs *flag.FlagSet) {
	fs.String("music", "", "the music laid under the voice: a track's id from `videos music list`, or none")
	fs.Float64("music-gain", 0, "move the music from its level 3 LU under the voice, -20 to 10 dB; needs -music")
}

// ref is a pointer to v, its zero value too: a flag given is sent as it is.
func ref[T any](v T) *T { return &v }

// lookLine is a look in a few words: the captions' preset, the frame and the background.
func lookLine(l openapi.VideoLook) string {
	line := "captions " + l.Captions.Preset
	if l.Captions.Position != openapi.PositionPreset {
		line += " " + string(l.Captions.Position)
	}

	line += ", " + string(l.Frame.Chrome)
	if l.FellBackFrom != nil {
		line += " (the phone turned: no " + string(*l.FellBackFrom) + " frame)"
	}

	if l.Frame.Chrome != openapi.ChromeFull && l.Background != nil {
		switch {
		case l.Background.Colors != nil:
			line += " on " + strings.Join(*l.Background.Colors, "-")
		case l.Background.Preset != nil:
			line += " on " + *l.Background.Preset
		}
	}

	if l.Music != nil {
		line += ", " + musicLine(*l.Music)
	}

	if c := l.Cards; c != nil {
		for _, k := range []struct {
			name string
			card *openapi.VideoCard
		}{{"title card", c.Title}, {"end card", c.End}} {
			if k.card != nil {
				line += fmt.Sprintf(", %s %q %.1f s", k.name, k.card.Title, float64(k.card.DurationMs)/1000)
			}
		}
	}

	return line
}

// musicLine is a look's music in a few words: the track's name and gain, or why it has none.
func musicLine(m openapi.VideoMusic) string {
	switch {
	case m.FellBackFrom != nil:
		return fmt.Sprintf("no music (%q was deleted)", m.FellBackFrom.Name)
	case m.Track == "none":
		return "no music"
	}

	line := "music " + cmp.Or(val(m.Name), m.Track)
	if m.GainDb != 0 {
		line += fmt.Sprintf(" %+g dB", m.GainDb)
	}

	return line
}

func videosLook(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	rev, err := revArg(fs.Arg(1))
	if err != nil {
		return err
	}

	look, err := lookArg(fs)
	if err != nil {
		return err
	}

	if look == nil {
		return fmt.Errorf("%w: give the parts of the look to change, such as --caption-preset, --chrome or --music",
			ErrUsage)
	}

	resp, err := e.api.ChangeVideoLookWithResponse(ctx, id, rev, &openapi.ChangeVideoLookParams{IdempotencyKey: *key(fs)},
		openapi.VideoLookChange{Look: look})
	if err != nil {
		return fmt.Errorf("changing the look of %s r%d: %w", id, rev, err)
	}

	if resp.JSON201 == nil {
		return apiError(fmt.Sprintf("changing the look of %s r%d", id, rev), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON201, func() { e.printVideo(resp.JSON201) })
}

func videosRetry(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	rev, err := revArg(fs.Arg(1))
	if err != nil {
		return err
	}

	resp, err := e.api.RetryVideoRevisionWithResponse(ctx, id, rev,
		&openapi.RetryVideoRevisionParams{IdempotencyKey: *key(fs)})
	if err != nil {
		return fmt.Errorf("making %s r%d again: %w", id, rev, err)
	}

	if resp.JSON201 == nil {
		return apiError(fmt.Sprintf("making %s r%d again", id, rev), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON201, func() { e.printVideo(resp.JSON201) })
}

func videosLooks(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListVideoLooksWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing the looks: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the looks", resp.StatusCode(), resp.JSONDefault)
	}

	c := resp.JSON200

	return e.print(fs, c, func() {
		presets := make([]string, 0, len(c.Presets))
		for _, p := range c.Presets {
			presets = append(presets, p.Id+" ("+p.Name+")")
		}

		chromes := make([]string, 0, len(c.Chromes))
		for _, ch := range c.Chromes {
			chromes = append(chromes, string(ch))
		}

		e.printf("caption presets: %s\n", strings.Join(presets, ", "))
		e.printf("chromes: %s\n", strings.Join(chromes, ", "))
		e.printf("backgrounds: %s\n", strings.Join(c.Backgrounds, ", "))
		e.printf("default: %s\n", lookLine(c.Default))
	})
}

func videosPreview(ctx context.Context, e *env, fs *flag.FlagSet) error {
	take := fs.Arg(0)
	body := openapi.VideoPreview{Take: take}

	if a := str(fs, "aspect"); a != "" {
		aspect := openapi.VideoPreviewAspect(a)
		body.Aspect = &aspect
	}

	if str(fs, "no-captions") == "true" {
		off := false
		body.Captions = &off
	}

	if r := str(fs, "rev"); r != "0" {
		rev, err := revArg(r)
		if err != nil {
			return err
		}

		body.Rev = &rev
	}

	if c := str(fs, "card"); c != "" {
		card := openapi.VideoPreviewCard(c)
		body.Card = &card
	}

	look, err := lookArg(fs)
	if err != nil {
		return err
	}

	body.Look = look

	name := take + "-look.jpg"
	if out := str(fs, "out"); out != "" {
		name = out
	}

	resp, err := e.api.PreviewVideoLookWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("making a look sample of %s: %w", take, err)
	}

	if resp.StatusCode() != http.StatusOK {
		return apiError("making a look sample of "+take, resp.StatusCode(), resp.JSONDefault)
	}

	// A new file that never replaces one, as a download is.
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a name of this directory
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s is already there and is not replaced; give another name with --out", name)
	}

	if err != nil {
		return fmt.Errorf("saving %s: %w", name, err)
	}

	_, err = f.Write(resp.Body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("saving %s: %w", name, err)
	}

	return e.print(fs, map[string]any{"file": name, "size": len(resp.Body)}, func() {
		e.printf("saved the look sample of %s as %s (%s)\n", take, name, size(int64(len(resp.Body))))
	})
}

// revArg reads a revision's number.
func revArg(s string) (int, error) {
	rev, err := strconv.Atoi(s)
	if err != nil || rev < 1 {
		return 0, fmt.Errorf("%w: a revision is a number from 1", ErrUsage)
	}

	return rev, nil
}

func videosApprove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	rev, err := revArg(fs.Arg(1))
	if err != nil {
		return err
	}

	resp, err := e.api.ApproveVideoRevisionWithResponse(ctx, id, rev)
	if err != nil {
		return fmt.Errorf("approving %s r%d: %w", id, rev, err)
	}

	if resp.JSON200 == nil {
		return apiError(fmt.Sprintf("approving %s r%d", id, rev), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printVideo(resp.JSON200) })
}

func videosRequestChanges(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	rev, err := revArg(fs.Arg(1))
	if err != nil {
		return err
	}

	body := openapi.VideoChanges{Note: optional(fs, "note")}

	if file := str(fs, "lines-file"); file != "" {
		raw, err := e.read(file)
		if err != nil {
			return err
		}

		lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
		body.Lines = &lines
	}

	if body.Note == nil && body.Lines == nil {
		return fmt.Errorf("%w: give --note, --lines-file or both", ErrUsage)
	}

	resp, err := e.api.RequestVideoChangesWithResponse(ctx, id, rev,
		&openapi.RequestVideoChangesParams{IdempotencyKey: key(fs)}, body)
	if err != nil {
		return fmt.Errorf("asking for changes to %s r%d: %w", id, rev, err)
	}

	if resp.JSON200 == nil {
		return apiError(fmt.Sprintf("asking for changes to %s r%d", id, rev), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printVideo(resp.JSON200) })
}

func videosDelete(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteVideoWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting video %s: %w", id, err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("deleting video "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]any{"deleted": id}, func() { e.printf("deleted video %s\n", id) })
}

func videosDownload(ctx context.Context, e *env, flags *flag.FlagSet) error {
	id := flags.Arg(0)

	rev, err := revArg(flags.Arg(1))
	if err != nil {
		return err
	}

	name := fmt.Sprintf("%s-r%d.mp4", id, rev)
	if out := str(flags, "out"); out != "" {
		name = out
	}

	resp, err := e.api.ReadVideoFile(ctx, id, rev, &openapi.ReadVideoFileParams{})
	if err != nil {
		return fmt.Errorf("downloading %s r%d: %w", id, rev, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

		return fmt.Errorf("downloading %s r%d: HTTP %d: %s", id, rev, resp.StatusCode, said)
	}

	// Written as it comes, to a new file that never replaces one, as a take's zip is.
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a name of this directory
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s is already there and is not replaced; give another name with --out", name)
	}

	if err != nil {
		return fmt.Errorf("saving %s: %w", name, err)
	}

	n, err := io.Copy(f, resp.Body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(name)

		return fmt.Errorf("saving %s: %w", name, err)
	}

	return e.print(flags, map[string]any{"file": name, "size": n}, func() {
		e.printf("saved %s r%d as %s (%s)\n", id, rev, name, size(n))
	})
}

func audioVoice(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, voice := fs.Arg(0), fs.Arg(1)

	resp, err := e.api.SetAudioVoiceWithResponse(ctx, openapi.AudioProvider(provider), openapi.AudioVoice{Voice: voice})
	if err != nil {
		return fmt.Errorf("setting %s's voice: %w", provider, err)
	}

	if resp.JSON200 == nil {
		return apiError("setting "+provider+"'s voice", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if voice == "" {
			e.printf("audio provider %s speaks videos with its own default voice\n", provider)

			return
		}

		e.printf("audio provider %s speaks videos with %s\n", provider, voice)
	})
}
