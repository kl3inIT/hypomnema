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
	// Aspect is 9:16 or 16:9; empty: 9:16.
	Aspect string
	// Captions; nil: on.
	Captions *bool
	// Look is the sections sent; the others are Rev's look, or the saved default's without Rev.
	Look *LookInput
	// Rev is a revision of the take's video the look starts from, as a look change does; 0: none.
	Rev int
}

// Preview makes a look sample, a JPEG.
func (s *Service) Preview(ctx context.Context, req PreviewRequest) ([]byte, error) {
	p, err := s.planPreview(ctx, req)
	if err != nil {
		return nil, err
	}

	wait := time.NewTimer(s.cfg.PreviewWait)
	defer wait.Stop()

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

	jpeg, err := s.sample(limited, p)
	if err != nil && ctx.Err() == nil && errors.Is(limited.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: longer than %s", ErrTimedOut, s.cfg.PreviewTimeout)
	}

	return jpeg, err
}

// previewPlan is what a look sample draws.
type previewPlan struct {
	take     devicetool.Take
	aspect   string
	captions bool
	look     Look
	line     string
}

// planPreview checks a sample's request and resolves its look as a make, or a look change from Rev, would: with the
// fallback of a take whose phone turned (row 11).
func (s *Service) planPreview(ctx context.Context, req PreviewRequest) (previewPlan, error) {
	if req.Aspect == "" {
		req.Aspect = string(compose.Portrait)
	}

	switch {
	case !takeIDs.MatchString(req.Take):
		return previewPlan{}, fmt.Errorf("%w: take is a take's id, such as tk_0a1b2c3d4e5f6a7b", ErrInvalid)
	case req.Aspect != string(compose.Portrait) && req.Aspect != string(compose.Landscape):
		return previewPlan{}, fmt.Errorf("%w: the aspect is 9:16 or 16:9", ErrInvalid)
	case req.Rev < 0:
		return previewPlan{}, fmt.Errorf("%w: a revision is a number from 1", ErrInvalid)
	}

	if _, err := resolveLook(req.Look, DefaultLook()); err != nil {
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

	base, line, err := s.previewBase(ctx, take.ID, req.Rev, scenes[0].Text)
	if err != nil {
		return previewPlan{}, err
	}

	look, err := resolveLook(req.Look, base)
	if err != nil {
		return previewPlan{}, err
	}

	if look, err = fitTake(look, req.Look != nil && req.Look.Frame != nil, take, req.Aspect); err != nil {
		return previewPlan{}, err
	}

	return previewPlan{take: take, aspect: req.Aspect, captions: req.Captions == nil || *req.Captions, look: look,
		line: line}, nil
}

// previewBase is the look a sample starts from and the line it shows: revision rev's, as a look change starts from
// it (#937); else the saved default, with the latest revision's first line, or the take's first scene's.
func (s *Service) previewBase(ctx context.Context, take string, rev int, first string) (Look, string, error) {
	v, err := s.OfTake(ctx, take)

	switch {
	case errors.Is(err, ErrNotFound) && rev == 0:
	case err != nil:
		return Look{}, "", err
	case rev > 0:
		for _, r := range v.Revisions {
			if r.Rev == rev && len(r.Lines) > 0 {
				return r.Look, r.Lines[0], nil
			}
		}

		return Look{}, "", fmt.Errorf("%w: revision %d of %s", ErrNotFound, rev, v.ID)
	default:
		if lines := v.Latest().Lines; len(lines) > 0 {
			first = lines[0]
		}
	}

	def, err := s.Looks(ctx)
	if err != nil {
		return Look{}, "", err
	}

	return def.Default, first, nil
}

// sample draws the look over the take's picture at its first step, with the line's captions at the start of its
// second word (or its first), the words timed at demotape's 14 characters a second (CaptionStyle.swift:153-180;
// CaptionBurner.swift:319-337), and no ring.
func (s *Service) sample(ctx context.Context, p previewPlan) ([]byte, error) {
	take, look := p.take, p.look

	tools, err := s.cfg.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: ffmpeg is not installed: %w", ErrUnavailable, err)
	}

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
