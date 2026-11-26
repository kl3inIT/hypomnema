// Package browsertaketest has a stand-in for the shared browser, whose tab's
// frames a test draws, for the tests of what films it (issue #1189).
package browsertaketest

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake"
)

// New is a Browser with no box.
func New() *Browser { return &Browser{Boxes: map[string]browser.Box{}} }

// Browser films a tab whose frames the test draws, and reads the boxes it was given.
type Browser struct {
	mu    sync.Mutex
	frame func(browser.Frame)
	// Last is the film started last.
	Last *Film
	// first are the frames the screencast draws as it starts; still is the tab's picture.
	First []browser.Frame
	Still []byte
	Boxes map[string]browser.Box
	// asked are the agents whose tab was filmed or boxed, and refuse the start's error.
	asked  []string
	Refuse error
}

// Film is the tab filmed; Stop it to have the tab go away.
type Film struct {
	size    browser.Size
	gone    chan struct{}
	still   []byte
	mu      sync.Mutex
	stopped bool
}

// Done is closed once the film stops.
func (f *Film) Done() <-chan struct{} { return f.gone }

// Size is the viewport filmed.
func (f *Film) Size() browser.Size { return f.size }

// Still is the tab's picture.
func (f *Film) Still(context.Context) ([]byte, error) { return f.still, nil }

// Stop stops the film, as the tab going away does.
func (f *Film) Stop(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.stopped {
		f.stopped = true
		close(f.gone)
	}
}

// Film starts filming, drawing First.
func (b *Browser) Film(_ context.Context, agent string, viewport browser.Size, frame func(browser.Frame),
) (browsertake.Film, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.asked = append(b.asked, agent)
	if b.Refuse != nil {
		return nil, b.Refuse
	}

	if viewport == (browser.Size{}) {
		viewport = browser.Size{Width: 1280, Height: 720}
	}

	b.frame = frame
	b.Last = &Film{size: viewport, gone: make(chan struct{}), still: b.Still}

	for _, fr := range b.First {
		frame(fr)
	}

	return b.Last, nil
}

// Box reads a box of Boxes.
func (b *Browser) Box(_ context.Context, agent, selector string) (browser.Box, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.asked = append(b.asked, agent)

	box, ok := b.Boxes[selector]
	if !ok {
		return browser.Box{}, errors.New("no such element")
	}

	return box, nil
}

// Draw has the screencast draw a frame.
func (b *Browser) Draw(fr browser.Frame) {
	b.mu.Lock()
	frame := b.frame
	b.mu.Unlock()

	frame(fr)
}

// Picture is a JPEG of a page of one color, w by h.
func Picture(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}

	var out bytes.Buffer
	require.NoError(t, jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}))

	return out.Bytes()
}
