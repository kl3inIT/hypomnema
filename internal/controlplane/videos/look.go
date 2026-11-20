package videos

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The look of a revision (issue #908, rows 1-3, 11 and 15; #937): how its captions look, the frame drawn around the
// phone's picture and the background it stands on. A look is sent by section; a section is explicit only when the
// request carries it, and the fields a sent section leaves out take the references' defaults (aidemo's frame,
// src/types.ts:538-562; demotape's Clean, CaptionStyle.swift:88; autodemo's midnight, compositor.html:28). The
// sections not sent come from a base: the saved default for a make, the revision a look change or a sample starts
// from. What is stored on the revision is always the resolved snapshot, which the runner draws as it is.

// Look is a revision's resolved look.
type Look struct {
	Captions CaptionsLook  `json:"captions"`
	Frame    compose.Frame `json:"frame"`
	// Background is absent only on the revisions made before the looks, which have no frame.
	Background *compose.Background `json:"background,omitempty"`
	// FellBackFrom is the chrome a frame taken from the default or the source would have drawn, on a take whose
	// phone turned: the revision is made full, with a warning (row 11).
	FellBackFrom compose.Chrome `json:"fell_back_from,omitempty"`
}

// CaptionsLook is a caption preset and where the captions sit.
type CaptionsLook struct {
	Preset   string            `json:"preset"`
	Position captions.Position `json:"position,omitempty"`
}

// legacyLook is the look of every revision made before the looks, as the migration wrote it.
var legacyLook = Look{Captions: CaptionsLook{Preset: captions.Legacy}, Frame: compose.Frame{Chrome: compose.ChromeFull}}

// LookInput is a look as a request sends it: each section, and each field of a section, may be left out.
type LookInput struct {
	Captions   *CaptionsInput   `json:"captions,omitempty"`
	Frame      *FrameInput      `json:"frame,omitempty"`
	Background *BackgroundInput `json:"background,omitempty"`
}

// CaptionsInput is the captions section as sent.
type CaptionsInput struct {
	Preset   *string `json:"preset,omitempty"`
	Position *string `json:"position,omitempty"`
}

// FrameInput is the frame section as sent.
type FrameInput struct {
	Chrome  *string `json:"chrome,omitempty"`
	Padding *int    `json:"padding,omitempty"`
	Radius  *int    `json:"radius,omitempty"`
	Shadow  *bool   `json:"shadow,omitempty"`
	SafeTop *bool   `json:"safe_top,omitempty"`
}

// BackgroundInput is the background section as sent: a preset or two colors.
type BackgroundInput struct {
	Preset *string   `json:"preset,omitempty"`
	Colors *[]string `json:"colors,omitempty"`
}

// canonical is the sections as sent, as a request's hash holds them: Go writes a struct's fields in order, and
// leaves out what was not sent.
func (in *LookInput) canonical() string {
	raw, _ := json.Marshal(in) //nolint:errchkjson // Strings, numbers and booleans.

	return string(raw)
}

// DefaultLook is the look the saved default starts as: Clean captions, an Android frame, the midnight background
// (row 15; android is an inference, as VMAP is filmed on Android).
func DefaultLook() Look {
	return Look{
		Captions:   CaptionsLook{Preset: captions.DefaultPreset, Position: captions.PositionPreset},
		Frame:      compose.DefaultFrame(compose.ChromeAndroid),
		Background: &compose.Background{Preset: compose.DefaultBackground},
	}
}

// parseLook reads a stored look; a revision's position left out (as the migration wrote it) is the preset's own.
func parseLook(raw string) (Look, error) {
	var l Look
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return Look{}, fmt.Errorf("reading a stored look: %w", err)
	}

	if l.Captions.Position == "" {
		l.Captions.Position = captions.PositionPreset
	}

	return l, nil
}

func (l Look) encode() string {
	raw, _ := json.Marshal(l) //nolint:errchkjson // Strings, numbers and booleans.

	return string(raw)
}

// captionsOf is the sent captions section with the references' defaults.
func captionsOf(in *CaptionsInput) (CaptionsLook, error) {
	out := CaptionsLook{Preset: captions.DefaultPreset, Position: captions.PositionPreset}
	if in.Preset != nil {
		out.Preset = *in.Preset
	}

	if in.Position != nil {
		out.Position = captions.Position(*in.Position)
	}

	if _, ok := captions.PresetByID(out.Preset); !ok {
		return CaptionsLook{}, fmt.Errorf("%w: no caption preset is named %q", ErrInvalid, out.Preset)
	}

	for _, p := range captions.Positions {
		if p == out.Position {
			return out, nil
		}
	}

	return CaptionsLook{}, fmt.Errorf("%w: no caption position is named %q", ErrInvalid, out.Position)
}

// frameOf is the sent frame section with aidemo's defaults: no chrome, padding 48, radius 40 for a device and 14
// otherwise, a shadow, no status strip. A radius sent is used as sent.
func frameOf(in *FrameInput) (compose.Frame, error) {
	chrome := compose.ChromeNone
	if in.Chrome != nil {
		chrome = compose.Chrome(*in.Chrome)
	}

	out := compose.DefaultFrame(chrome)
	if in.Padding != nil {
		out.Padding = *in.Padding
	}

	if in.Radius != nil {
		out.Radius = *in.Radius
	}

	if in.Shadow != nil {
		out.Shadow = *in.Shadow
	}

	if in.SafeTop != nil {
		out.SafeTop = *in.SafeTop
	}

	if chrome == "" {
		return compose.Frame{}, fmt.Errorf("%w: a frame's chrome is full, none, android or iphone", ErrInvalid)
	}

	if err := out.Check(); err != nil {
		return compose.Frame{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return out, nil
}

// backgroundOf is the sent background section: a preset, two colors, or midnight when it holds neither. Colors
// sent empty are refused, not read as none (#937, the review of #936).
func backgroundOf(in *BackgroundInput) (*compose.Background, error) {
	out := &compose.Background{}
	if in.Preset != nil {
		out.Preset = *in.Preset
	}

	if in.Colors != nil {
		if len(*in.Colors) != 2 {
			return nil, fmt.Errorf("%w: a background has two colors, not %d", ErrInvalid, len(*in.Colors))
		}

		out.Colors = append([]string{}, *in.Colors...)
	}

	if out.Preset == "" && out.Colors == nil {
		out.Preset = compose.DefaultBackground
	}

	if err := out.Check(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return out, nil
}

// resolveLook is the look of a new revision: each section sent, with its defaults, else the base's; a background
// neither sent nor in the base (a revision made before the looks) is midnight.
func resolveLook(in *LookInput, base Look) (Look, error) {
	out := base
	if in == nil {
		in = &LookInput{}
	}

	if in.Captions != nil {
		c, err := captionsOf(in.Captions)
		if err != nil {
			return Look{}, err
		}

		out.Captions = c
	}

	if in.Frame != nil {
		f, err := frameOf(in.Frame)
		if err != nil {
			return Look{}, err
		}

		out.Frame, out.FellBackFrom = f, ""
	}

	if in.Background != nil {
		bg, err := backgroundOf(in.Background)
		if err != nil {
			return Look{}, err
		}

		out.Background = bg
	} else if out.Background == nil {
		out.Background = &compose.Background{Preset: compose.DefaultBackground}
	} else {
		bg := *out.Background
		out.Background = &bg
	}

	return out, nil
}

// fitTake keeps a framed look only where the take can be framed (row 11): a frame sent for a take whose phone
// turned is refused; one taken from the default or the source is stored full, with the chrome it fell back from.
func fitTake(l Look, sentFrame bool, take devicetool.Take, aspect string) (Look, error) {
	err := compose.Frameable(take, l.Frame, background(l), compose.Aspect(aspect))

	switch {
	case err == nil:
		return l, nil
	case sentFrame && errors.Is(err, compose.ErrTurnedTake):
		return Look{}, fmt.Errorf("%w: %w", ErrConflict, err)
	case sentFrame:
		return Look{}, fmt.Errorf("%w: the take cannot be framed: %w", ErrConflict, err)
	}

	l.FellBackFrom, l.Frame.Chrome = l.Frame.Chrome, compose.ChromeFull

	return l, nil
}

// mergeDefault is the saved default with the sections a make sent, resolved, in place of its own (row 3).
func mergeDefault(saved Look, in *LookInput, resolved Look) Look {
	out := saved
	out.FellBackFrom = ""

	if in.Captions != nil {
		out.Captions = resolved.Captions
	}

	if in.Frame != nil {
		out.Frame = resolved.Frame
	}

	if in.Background != nil {
		out.Background = resolved.Background
	}

	return out
}

func background(l Look) compose.Background {
	if l.Background == nil {
		return compose.Background{}
	}

	return *l.Background
}

// fellBackWarning is the warning of a revision whose frame fell back to full.
func fellBackWarning(chrome compose.Chrome) string {
	return fmt.Sprintf("the phone turned during the take, so the video has no %s frame", chrome)
}
