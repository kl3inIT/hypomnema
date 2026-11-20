package captions

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// A font of the captions and what libass sizes it by (issue #908, rows 7 and 8).
//
// libass gives a style's Fontsize to the font's usWinAscent + usWinDescent, as GDI does
// (libass a6613d2, libass/ass_font.c: set_font_metrics, lines 355-365, and ass_face_set_size, lines 585-594): a font
// whose em is to be e pixels is asked for at e × (usWinAscent + usWinDescent) / unitsPerEm. Those come from the
// OS/2 and head tables, which x/image's sfnt does not expose, so a small reader takes them from the file; the text
// is estimated with sfnt as libass lays it out (span), kerning off (its default, Kerning: no), and then measured as
// libass draws it (fit.go).

// metrics are a font's sizes in its own units.
type metrics struct {
	UnitsPerEm int
	WinAscent  int
	WinDescent int
}

// readMetrics reads unitsPerEm from the head table and usWinAscent and usWinDescent from the OS/2 table.
func readMetrics(file []byte) (metrics, error) {
	head, err := sfntTable(file, "head")
	if err != nil {
		return metrics{}, err
	}

	os2, err := sfntTable(file, "OS/2")
	if err != nil {
		return metrics{}, err
	}

	// head: unitsPerEm at offset 18; OS/2: usWinAscent at 74, usWinDescent at 76 (OpenType, version 0 on).
	if len(head) < 20 || len(os2) < 78 {
		return metrics{}, errors.New("the font's head or OS/2 table is too short")
	}

	m := metrics{
		UnitsPerEm: int(binary.BigEndian.Uint16(head[18:])),
		WinAscent:  int(binary.BigEndian.Uint16(os2[74:])),
		WinDescent: int(binary.BigEndian.Uint16(os2[76:])),
	}
	if m.UnitsPerEm == 0 || m.WinAscent+m.WinDescent == 0 {
		return metrics{}, errors.New("the font has no usable size")
	}

	return m, nil
}

// sfntTable is a table of a TrueType file by its tag, from the table directory.
func sfntTable(file []byte, tag string) ([]byte, error) {
	if len(file) < 12 {
		return nil, errors.New("not a font file")
	}

	n := int(binary.BigEndian.Uint16(file[4:]))

	for i := range n {
		at := 12 + 16*i
		if at+16 > len(file) {
			break
		}

		rec := file[at : at+16]
		if string(rec[:4]) != tag {
			continue
		}

		off, length := int(binary.BigEndian.Uint32(rec[8:])), int(binary.BigEndian.Uint32(rec[12:]))
		if off < 0 || length < 0 || off+length > len(file) {
			return nil, fmt.Errorf("the font's %s table is outside the file", tag)
		}

		return file[off : off+length], nil
	}

	return nil, fmt.Errorf("the font has no %s table", tag)
}

// face is a font of the captions, parsed.
type face struct {
	metrics

	font *sfnt.Font
}

// assSize is the ASS Fontsize that gives an em of em pixels.
func (f *face) assSize(em float64) float64 {
	return em * float64(f.WinAscent+f.WinDescent) / float64(f.UnitsPerEm)
}

// shapeSize is the Fontsize libass shapes and loads every glyph at, with hinting off, before scaling it to its
// own: "not too small to prevent grid fitting rounding effects" (libass/ass_render.c, fix_glyph_scaling, lines
// 1981-2002; ffmpeg's subtitles filter leaves libass's hinting at none).
const shapeSize = 256

// span is a line's width in pixels, set at an ASS Fontsize of size and squeezed to fx, estimated as libass lays it
// out: each glyph's advance at shapeSize rounded to a whole pixel (libass loads it hinted there: ass_shaper.c,
// ass_glyph_metrics_construct, lines 253-274), then scaled by fx × size / shapeSize and rounded to 1/64 px, one
// glyph at a time (ass_shaper.c, line 656); its ink the unhinted outline, scaled alike (ass_font.c, lines 782-786).
// From the left of the first glyph's ink or of the pen, whichever is further left, to the right of the furthest
// ink or of the pen after the last advance. It is the layout's first guess: the TrueType hinter moves some advances
// by a pixel more (ExtraBold's N: 126.48 to 127), which only the font's bytecode knows, so what libass draws is
// measured after (fit.go).
func (f *face) span(text string, size, fx float64) (float64, error) {
	var (
		buf         sfnt.Buffer
		pen         int64 // 26.6
		left, right = 0.0, 0.0
	)

	units := fixed.I(f.UnitsPerEm) // sfnt at a ppem of unitsPerEm gives the font's own units, in 26.6.
	height := int64(f.WinAscent + f.WinDescent)
	// FreeType's x scale for a size request of shapeSize (FT_SIZE_REQUEST_TYPE_REAL_DIM, height only), 16.16.
	xScale := (int64(shapeSize*64)<<16 + height/2) / height
	scale := fx * size / shapeSize
	inkPx := fx * size / float64(height) // a font unit of the outline, in pixels

	for _, r := range text {
		idx, err := f.font.GlyphIndex(&buf, r)
		if err != nil {
			return 0, err
		}

		bounds, advance, err := f.font.GlyphBounds(&buf, idx, units, font.HintingNone)
		if err != nil {
			return 0, err
		}

		at := float64(pen) / 64
		if bounds.Max.X > bounds.Min.X {
			left = math.Min(left, at+float64(bounds.Min.X)/64*inkPx)
			right = math.Max(right, at+float64(bounds.Max.X)/64*inkPx)
		}

		// FT_MulFix, then FT_PIX_ROUND: the hinted advance at shapeSize, in 26.6; then libass's ass_lrint.
		shaped := (int64(advance)/64*xScale + 0x8000) >> 16
		shaped = (shaped + 32) &^ 63
		pen += int64(math.RoundToEven(float64(shaped) * scale))
	}

	return math.Max(right, float64(pen)/64) - left, nil
}

// has says whether the font has a glyph for a character.
func (f *face) has(r rune) bool {
	var buf sfnt.Buffer

	idx, err := f.font.GlyphIndex(&buf, r)

	return err == nil && idx != 0
}

// fontFiles are the fonts the presets use, by their full name, and their files as the ffmpeg install writes them.
var fontFiles = map[string]string{
	fontExtraBold: "BeVietnamPro-ExtraBold.ttf",
	fontBold:      "BeVietnamPro-Bold.ttf",
	fontMedium:    "BeVietnamPro-Medium.ttf",
	fontMono:      "JetBrainsMono-SemiBold.ttf",
}

var (
	facesOnce sync.Once
	faces     map[string]*face
	errFaces  error
)

// faceOf is a preset's font, parsed once.
func faceOf(name string) (*face, error) {
	facesOnce.Do(func() {
		faces = map[string]*face{}

		for full, file := range fontFiles {
			f, err := loadFace(file)
			if err != nil {
				errFaces = fmt.Errorf("the font %s: %w", file, err)

				return
			}

			faces[full] = f
		}
	})

	if errFaces != nil {
		return nil, errFaces
	}

	f, ok := faces[name]
	if !ok {
		return nil, fmt.Errorf("no font is named %q", name)
	}

	return f, nil
}

func loadFace(file string) (*face, error) {
	raw, err := ffmpeg.FontFile(file)
	if err != nil {
		return nil, err
	}

	m, err := readMetrics(raw)
	if err != nil {
		return nil, err
	}

	parsed, err := sfnt.Parse(raw)
	if err != nil {
		return nil, err
	}

	return &face{metrics: m, font: parsed}, nil
}
