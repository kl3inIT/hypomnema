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
// is measured with sfnt, unhinted, as libass lays it out with hinting off and kerning off (its default, Kerning: no).

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

// span is a line's width in pixels at an em of em pixels: from the left of its first glyph's ink or of its
// pen, whichever is further left, to the right of the furthest ink or of the pen after its last advance. libass
// centers a line on its advances, and a glyph's ink may reach past them (a W's arms); the cap counts both.
func (f *face) span(text string, em float64) (float64, error) {
	var (
		buf         sfnt.Buffer
		pen         fixed.Int26_6
		left, right = 0.0, 0.0
	)

	ppem := fixed.Int26_6(math.Round(em * 64))

	for _, r := range text {
		idx, err := f.font.GlyphIndex(&buf, r)
		if err != nil {
			return 0, err
		}

		bounds, advance, err := f.font.GlyphBounds(&buf, idx, ppem, font.HintingNone)
		if err != nil {
			return 0, err
		}

		if bounds.Max.X > bounds.Min.X {
			left = math.Min(left, float64(pen+bounds.Min.X)/64)
			right = math.Max(right, float64(pen+bounds.Max.X)/64)
		}

		pen += advance
	}

	return math.Max(right, float64(pen)/64) - left, nil
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
