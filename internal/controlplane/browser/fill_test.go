package browser

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A pane's page is its shape at least a desktop wide and at most twice as
// tall as wide, as OpenMausBot's fillPage; a pane out of bounds gives none.
func TestFillPage(t *testing.T) {
	for _, c := range []struct {
		name          string
		width, height float64
		page          Size
		ok            bool
	}{
		{name: "a wide pane is its own size", width: 1600.4, height: 900, page: Size{1600, 900}, ok: true},
		{name: "a narrow pane keeps a desktop's width", width: 640, height: 480, page: Size{1280, 960}, ok: true},
		{name: "a tall pane is cut at twice its width", width: 390, height: 844, page: Size{1280, 2560}, ok: true},
		{name: "a collapsed pane", width: 0, height: 0},
		{name: "a pane too large", width: 5000, height: 900},
		{name: "not a number", width: math.NaN(), height: 900},
	} {
		t.Run(c.name, func(t *testing.T) {
			page, ok := fillPage(c.width, c.height)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.page, page)
		})
	}
}
