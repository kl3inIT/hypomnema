package videos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The look sample (issue #908, row 14): a JPEG of the chosen look over the take's picture at its first step, drawn
// by compose.Still with the export's layout, frame PNG and captions, as Cap previews through its real renderer
// (crates/editor/src/editor_instance.rs:1731). It shows the look; it does not promise the video shows that moment,
// which the video may cut. One is made at a time.

// The look sample's limits (row 14; inference).
const (
	// PreviewWait is how long a sample waits for the one being made before it is refused as busy.
	PreviewWait = 5 * time.Second
	// PreviewTimeout bounds the making of one sample.
	PreviewTimeout = 20 * time.Second

	previewDir = ".preview"
)

// Errors of the look sample.
var (
	// ErrBusy refuses a sample while another is made for longer than PreviewWait; try again in a second.
	ErrBusy = errors.New("another look sample is being made; try again in a second")
	// ErrTimedOut reports a sample that took longer than PreviewTimeout.
	ErrTimedOut = errors.New("the look sample took too long")
)

// PreviewRequest asks for a look sample of a take.
type PreviewRequest struct {
	Take string
	// Aspect is 9:16, 16:9 or 886x1920; empty: the template's first, else 9:16.
	Aspect string
	// Captions; nil: the template's, else on.
	Captions *bool
	// Look is the sections sent; the others are Rev's look, or the template's and the saved default's without Rev.
	Look *LookInput
	// Template is a template's id, its look resolved as a make's (issue #1230, row 5); never with Rev.
	Template string
	// Rev is a revision of the take's video the look starts from, as a look change does; 0: none.
	Rev int
	// Card is a card of the look to draw in place of the take's picture: title or end; empty: the picture (issue
	// #1230, row 1).
	Card string
}

// The cards a sample draws.
const (
	CardTitle = "title"
	CardEnd   = "end"
)

// Preview makes a look sample, a JPEG. What needs no reading is checked first; then, without waiting for an
// install, that ffmpeg is in place; then it waits at most PreviewWait from the request's start for the one being
// made, and has PreviewTimeout from there to read the take and draw (review of #1001).
func (s *Service) Preview(ctx context.Context, req PreviewRequest) ([]byte, error) {
	wait := time.NewTimer(s.cfg.PreviewWait)
	defer wait.Stop()

	if _, _, err := checkPreview(req); err != nil {
		return nil, err
	}

	tools, err := s.readyTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: ffmpeg is not installed: %w", ErrUnavailable, err)
	}

	select {
	case s.preview <- struct{}{}:
	case <-wait.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	defer func() { <-s.preview }()

	limited, cancel := context.WithTimeout(ctx, s.cfg.PreviewTimeout)
	defer cancel()

	jpeg, err := func() ([]byte, error) {
		p, err := s.planPreview(limited, req)
		if err != nil {
			return nil, err
		}

		if err := limited.Err(); err != nil {
			return nil, err
		}

		return s.sample(limited, p, tools)
	}()
	if err != nil && ctx.Err() == nil && errors.Is(limited.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: longer than %s", ErrTimedOut, s.cfg.PreviewTimeout)
	}

	return jpeg, err
}

// readyTools is the installed ffmpeg without waiting for an install under way (Config.Ready), else Tools.
func (s *Service) readyTools(ctx context.Context) (ffmpeg.Tools, error) {
	if s.cfg.Ready != nil {
		return s.cfg.Ready()
	}

	return s.cfg.Tools(ctx)
}

// checkPreview checks what a sample's request says, with no reading: the take's id, the aspect, the revision and
// the look's ids and ranges.
func checkPreview(req PreviewRequest) (PreviewRequest, *Template, error) {
	tpl, err := templateOf(req.Template)
	if err != nil {
		return req, nil, err
	}

	if tpl != nil && req.Rev > 0 {
		return req, nil, fmt.Errorf("%w: a template starts a make, not a look change of a revision", ErrInvalid)
	}

	if req.Aspect, err = aspectWith(tpl, req.Aspect); err != nil {
		return req, nil, err
	}

	if req.Aspect == "" {
		req.Aspect = string(compose.Portrait)
	}

	req.Captions = captionsWith(tpl, req.Captions)

	switch {
	case !takeIDs.MatchString(req.Take):
		return req, nil, fmt.Errorf("%w: take is a take's id, such as tk_0a1b2c3d4e5f6a7b", ErrInvalid)
	case !knownAspect(req.Aspect):
		return req, nil, fmt.Errorf("%w: the aspect is 9:16, 16:9 or 886x1920", ErrInvalid)
	case req.Rev < 0:
		return req, nil, fmt.Errorf("%w: a revision is a number from 1", ErrInvalid)
	case req.Card != "" && req.Card != CardTitle && req.Card != CardEnd:
		return req, nil, fmt.Errorf("%w: a sample's card is title or end", ErrInvalid)
	}

	req.Look = onlyCard(req.Look, req.Card)

	if _, err := resolveLook(req.Look, DefaultLook()); err != nil {
		return req, nil, err
	}

	return req, tpl, nil
}

// onlyCard is the look a sample of one card resolves: the sent cards section holding that card alone, so the other
// one, turned on and not titled yet, is not checked (the review of #1242). Any other sample's look is as sent.
func onlyCard(in *LookInput, card string) *LookInput {
	if card == "" || in == nil || in.Cards == nil {
		return in
	}

	look, cards := *in, *in.Cards
	if card == CardTitle {
		cards.End = nil
	} else {
		cards.Title = nil
	}

	look.Cards = &cards

	return &look
}

// previewPlan is what a look sample draws.
type previewPlan struct {
	take     devicetool.Take
	aspect   string
	captions bool
	look     Look
	line     string
	// card is the look's card to draw, nil for the take's picture.
	card *compose.Card
}

// planPreview checks a sample's request and resolves its look as a make, or a look change from Rev, would: with the
// fallback of a take whose phone turned (row 11).
func (s *Service) planPreview(ctx context.Context, req PreviewRequest) (previewPlan, error) {
	req, tpl, err := checkPreview(req)
	if err != nil {
		return previewPlan{}, err
	}

	take, err := s.cfg.Takes.Get(req.Take)
	if err != nil {
		return previewPlan{}, fmt.Errorf("%w: take %s", ErrNotFound, req.Take)
	}

	if take.State != devicetool.TakeAvailable {
		return previewPlan{}, fmt.Errorf("%w: take %s is %s, not available", ErrConflict, take.ID, take.State)
	}

	scenes, err := compose.Scenes(take)
	if err != nil {
		return previewPlan{}, fmt.Errorf("%w: %w", ErrConflict, err)
	}

	base, line, title, err := s.previewBase(ctx, take, req.Rev, scenes[0].Text)
	if err != nil {
		return previewPlan{}, err
	}

	look, err := resolveLook(lookWith(tpl, title, req.Look), base)
	if err != nil {
		return previewPlan{}, err
	}

	if look, err = fitTake(look, req.Look != nil && req.Look.Frame != nil, take, req.Aspect); err != nil {
		return previewPlan{}, err
	}

	p := previewPlan{take: take, aspect: req.Aspect, captions: req.Captions == nil || *req.Captions, look: look,
		line: line}

	if req.Card != "" {
		if look.Cards != nil {
			p.card = map[string]*compose.Card{CardTitle: look.Cards.Title, CardEnd: look.Cards.End}[req.Card]
		}

		if p.card == nil {
			return previewPlan{}, fmt.Errorf("%w: the look has no %s card", ErrConflict, req.Card)
		}
	}

	return p, nil
}

// previewBase is the look a sample starts from, the line it shows and the title a template's cards take: revision
// rev's, as a look change starts from it (#937); else the saved default, with the latest revision's first line, or
// the take's first scene's; the video's title, else the take's, as a make titles them.
func (s *Service) previewBase(ctx context.Context, take devicetool.Take, rev int, first string,
) (Look, string, string, error) {
	title := take.Title
	v, err := s.OfTake(ctx, take.ID)

	switch {
	case errors.Is(err, ErrNotFound) && rev == 0:
	case err != nil:
		return Look{}, "", "", err
	case rev > 0:
		for _, r := range v.Revisions {
			if r.Rev == rev && len(r.Lines) > 0 {
				return r.Look, r.Lines[0], v.Title, nil
			}
		}

		return Look{}, "", "", fmt.Errorf("%w: revision %d of %s", ErrNotFound, rev, v.ID)
	default:
		title = v.Title
		if lines := v.Latest().Lines; len(lines) > 0 {
			first = lines[0]
		}
	}

	def, err := s.Looks(ctx)
	if err != nil {
		return Look{}, "", "", err
	}

	return def.Default, first, title, nil
}

// sample draws the look over the take's picture at its first step, with the line's captions at the start of its
// second word (or its first), the words timed at demotape's 14 characters a second (CaptionStyle.swift:153-180;
// CaptionBurner.swift:319-337), and no ring.
func (s *Service) sample(ctx context.Context, p previewPlan, tools ffmpeg.Tools) ([]byte, error) {
	take, look := p.take, p.look

	takeJSON, err := s.cfg.Takes.File(take.ID, "take.json")
	if err != nil {
		return nil, fmt.Errorf("%w: take %s has no files: %w", ErrConflict, take.ID, err)
	}

	root := filepath.Join(s.cfg.Dir, previewDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp(root, "sample-")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.RemoveAll(work) }()

	if p.card != nil {
		// A card as the video shows it between its fades, on the look's background (#1230, row 1).
		out := filepath.Join(work, "sample.jpg")
		if err := compose.CardStill(ctx, tools, compose.CardStillInput{
			Card: *p.card, Accent: look.Cards.Accent, Background: background(look), Aspect: compose.Aspect(p.aspect),
			Work: filepath.Join(work, "card"), Out: out,
		}); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			return nil, err
		}

		return os.ReadFile(out) //nolint:gosec // The sample's own work folder.
	}

	in := compose.StillInput{
		Dir: filepath.Dir(takeJSON), Aspect: compose.Aspect(p.aspect), Frame: look.Frame, Background: background(look),
		Work: filepath.Join(work, "still"), Out: filepath.Join(work, "sample.jpg"),
	}

	if p.captions {
		if in.Captions, in.CaptionsAtMs, err = sampleCaptions(ctx, tools, look.Captions, p.line, in.Aspect,
			filepath.Join(work, "captions.ass")); err != nil {
			return nil, err
		}
	}

	if err := compose.Still(ctx, tools, in); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, err
	}

	return os.ReadFile(in.Out)
}

// sampleCaptions writes the line's captions in the look to out, and says the moment the sample shows them at: the
// start of the second word, else the first. The words' times are on ASS's centiseconds, so that moment is exactly
// where a word changes.
func sampleCaptions(ctx context.Context, tools ffmpeg.Tools, look CaptionsLook, line string, aspect compose.Aspect,
	out string,
) (string, int64, error) {
	size, err := aspect.Size()
	if err != nil {
		return "", 0, err
	}

	floor := captions.HighlightMinSeconds
	if p, ok := captions.PresetByID(look.Preset); ok && p.WordByWord() {
		floor = captions.WordByWordMinSeconds
	}

	const sceneSeconds = 3600

	words := captions.SynthesizeWords(line, 0, sceneSeconds, captions.CharactersPerSecond, floor)
	if len(words) == 0 {
		return "", 0, fmt.Errorf("%w: the line has no word", ErrConflict)
	}

	for k := range words {
		words[k].Start, words[k].End = math.Round(words[k].Start*100)/100, math.Round(words[k].End*100)/100
	}

	cues, err := captions.Build([]captions.Scene{{StartMs: 0, EndMs: sceneSeconds * 1000, Words: words}},
		captions.Default)
	if err != nil {
		return "", 0, err
	}

	ass, err := captions.Render(ctx, tools, cues, captions.Look{Preset: look.Preset, Position: look.Position},
		size.Width, size.Height, nil)
	if err != nil {
		return "", 0, err
	}

	if err := os.WriteFile(out, []byte(ass), 0o600); err != nil {
		return "", 0, err
	}

	at := words[0].Start
	if len(words) > 1 {
		at = words[1].Start
	}

	return out, int64(math.Round(at * 1000)), nil
}
