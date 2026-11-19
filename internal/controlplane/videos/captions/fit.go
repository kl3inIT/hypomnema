package captions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// The width cap, as drawn (issue #908, row 8; #935). libass lays a line out with advances that FreeType's
// TrueType hinter gives at the size it shapes at (libass/ass_shaper.c, lines 253-274, and ass_render.c, lines
// 1981-2002), which a reader of the font cannot know without the font's own bytecode run: ExtraBold's N is 126.48 px
// unhinted and 127 as libass shapes it, Bold's I 48.66 and 50. So every block is drawn once, by the pinned ffmpeg's
// libass, the width of what it drew read back, and a block wider than the cap made smaller and drawn again, until
// each fits: the block is let through only once libass has drawn it within 0.94 of the width.

// maxFitRounds bounds the rounds of drawing and shrinking; each narrows a block that does not fit by its measured
// excess, so a few are enough.
const maxFitRounds = 12

// fit draws every block of every cue with libass, where it is placed on the video, and shrinks each that is wider
// than the cap until it fits. A block is drawn on the whole frame at its own place, as the video shows it: a glyph's
// ink may reach far past its line (marks stacked on a letter), and what reaches past the frame is not drawn. A
// shrink makes a block lower, which may move its cue (a ring's flip): every block whose place changed is drawn
// again, until none is too wide and none has moved since it was drawn.
func (r *renderer) fit(ctx context.Context, tools ffmpeg.Tools, cues []Cue, laid [][]frame, chosen Position,
	rings []compose.Ring,
) error {
	limit := math.Floor(r.w * SafeFraction)
	measuredAt := map[*block]float64{}

	for round := 0; ; round++ {
		placed := r.placeCues(cues, laid, chosen, rings)

		var (
			pending []*block
			tops    []float64
			n       int
		)

		for k := range laid {
			for i := range laid[k] {
				b, top := &laid[k][i].block, placed[n].top
				n++

				if at, ok := measuredAt[b]; !ok || at != top {
					pending, tops = append(pending, b), append(tops, top)
				}
			}
		}

		if len(pending) == 0 {
			return nil
		}

		if round == maxFitRounds {
			return fmt.Errorf("captions: %d blocks are still wider than %.0f px or moving after %d rounds",
				len(pending), limit, maxFitRounds)
		}

		widths, err := r.drawnWidths(ctx, tools, pending, tops)
		if err != nil {
			return err
		}

		for i, b := range pending {
			b.drawnText = widths[i]
			b.w = r.width(*b)

			if r.drawn(*b) <= limit {
				measuredAt[b] = tops[i]

				continue
			}

			smaller, err := r.shrink(*b, limit)
			if err != nil {
				return err
			}

			*b = smaller

			delete(measuredAt, b)
		}
	}
}

// shrink makes a block that libass drew wider than limit smaller by its excess: its em first, down to the floor,
// then its squeeze, as layout does; a little more each round, so that the rounds end.
func (r *renderer) shrink(b block, limit float64) (block, error) {
	d := r.drawn(b)

	// What does not scale with the squeeze: a box's padding and edges, or the outline and the text's edges.
	fixedPx := 2*b.outline + inkEdgePx
	if r.p.HasBox() && !r.p.Bar {
		fixedPx = 2*b.padX + 2*inkEdgePx
	}

	em, fx := b.em, b.fx

	if q := limit / d * 0.995; em > MinFontSize && em*q >= MinFontSize {
		em *= q
	} else {
		if em > MinFontSize {
			// The part of the excess the floor leaves to the squeeze.
			d = fixedPx + (d-fixedPx)*MinFontSize/em
			em = MinFontSize
		}

		fx *= (limit - fixedPx) / (d - fixedPx) * 0.995
		fx = math.Floor(fx*1e4) / 1e4
	}

	if fx <= 0.01 {
		return block{}, errors.New("captions: a block cannot be squeezed to fit the width")
	}

	return r.measure(b.lines, em, fx)
}

// drawnWidths draws each block's lines alone, one a frame, with the pinned ffmpeg's libass, on a grey frame the
// video's size with its top at tops[i], and returns how many columns each drew in: its text, outline and
// anti-aliased edges, all of the ink that the frame shows.
func (r *renderer) drawnWidths(ctx context.Context, tools ffmpeg.Tools, blocks []*block, tops []float64,
) ([]float64, error) {
	if tools.FFmpeg == "" {
		return nil, errors.New("captions: ffmpeg is needed to fit the captions to the video's width")
	}

	dir, err := os.MkdirTemp("", "hypomnema-captions-")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	var events strings.Builder

	for i, b := range blocks {
		events.WriteString(r.textEvents(*b, tops[i], assTime(float64(i)), assTime(float64(i+1))))
	}

	width, height := int(r.w), int(r.h)

	if err := os.WriteFile(filepath.Join(dir, "measure.ass"), []byte(r.header(width, height)+events.String()),
		0o600); err != nil {
		return nil, err
	}

	if err := os.Symlink(tools.FontsDir, filepath.Join(dir, "fonts")); err != nil {
		return nil, err
	}

	// One frame a second, each block's events in its second: frame i shows block i. Relative names, read in the
	// folder, need no escaping in a filter's option.
	//nolint:gosec // The pinned ffmpeg, its arguments ours.
	cmd := exec.CommandContext(ctx, tools.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x808080:s=%dx%d:r=1:d=%d", width, height, len(blocks)),
		"-vf", "format=rgb24,subtitles=filename=measure.ass:fontsdir=fonts",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	widths, readErr := readWidths(bufio.NewReaderSize(out, 1<<20), width, height, len(blocks))
	if readErr != nil {
		_, _ = io.Copy(io.Discard, out)
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("captions: drawing the blocks to measure them: %w: %s", err, firstLine(stderr.String()))
	}

	return widths, readErr
}

// readWidths reads n frames of w × h rgb24 and returns, for each, the columns from its first to its last pixel that
// is not the grey; 0 for a frame with none.
func readWidths(in io.Reader, w, h, n int) ([]float64, error) {
	frame := make([]byte, w*h*3)
	grey := bytes.Repeat([]byte{0x80}, w*3)
	out := make([]float64, 0, n)

	for range n {
		if _, err := io.ReadFull(in, frame); err != nil {
			return nil, fmt.Errorf("captions: reading the drawn blocks: %w", err)
		}

		first, last := w, -1

		for y := range h {
			row := frame[y*w*3 : (y+1)*w*3]
			if bytes.Equal(row, grey) {
				continue
			}

			for x := range w {
				if row[3*x] != 0x80 || row[3*x+1] != 0x80 || row[3*x+2] != 0x80 {
					first, last = min(first, x), max(last, x)
				}
			}
		}

		out = append(out, float64(max(0, last-first+1)))
	}

	return out, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")

	return line
}
