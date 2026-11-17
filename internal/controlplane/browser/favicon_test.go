package browser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const anIcon = "data:image/png;base64,iVBORw0KGgo="

// The page reads its icon in a world of the engine's own, on the tab's
// session, and the engine leaves the tab again.
func TestAPageReadsItsIconInAWorldOfItsOwn(t *testing.T) {
	t.Parallel()

	chrome := &fakeChrome{answer: func(method, _ string) (string, []string) {
		switch method {
		case "Target.attachToTarget":
			return `{"sessionId":"S"}`, nil
		case "Page.createIsolatedWorld":
			return `{"executionContextId":7}`, nil
		case "Runtime.evaluate":
			return `{"result":{"type":"string","value":"` + anIcon + `"}}`, nil
		}

		return `{}`, nil
	}}

	assert.Equal(t, anIcon, readFavicon(t.Context(), chrome.serve(t), "T1"))

	sent := chrome.sent()
	assert.Equal(t, []string{"Page.createIsolatedWorld", "Runtime.evaluate"}, chrome.methods("S"))
	assert.Contains(t, sent[0], `Target.attachToTarget {"flatten":true,"targetId":"T1"}`)
	assert.Contains(t, sent[1], `"frameId":"T1"`)
	assert.Contains(t, sent[2], `"contextId":7`)
	assert.Contains(t, sent[2], `})(65536, 10000)`)
	assert.Contains(t, sent[len(sent)-1], `Target.detachFromTarget {"sessionId":"S"}`)
}

// What a page answers is taken only as a small image's data: address.
func TestAnIconIsADataAddressOfAnImage(t *testing.T) {
	t.Parallel()

	assert.Equal(t, anIcon, faviconData(anIcon))
	assert.Equal(t, "data:image/svg+xml;base64,PHN2Zz4=", faviconData("data:image/svg+xml;base64,PHN2Zz4="))

	for _, refused := range []string{
		"",
		"https://example.com/favicon.ico",
		"data:text/html;base64,PGI+",
		"data:image/png,<svg>",
		`data:image/png" onerror="x;base64,AAAA`,
		"data:image/png;base64,AA AA",
		"data:image/png;base64,",
		"data:image/png;base64," + strings.Repeat("A", faviconBytes*2),
	} {
		assert.Empty(t, faviconData(refused), refused)
	}
}

// A site's icon is read once, while no read of it is under way, and kept,
// even when there was none.
func TestASitesIconIsReadOnceAndKept(t *testing.T) {
	t.Parallel()

	var f favicons

	_, known, read := f.icon("https://example.com")
	assert.False(t, known)
	assert.True(t, read, "the first to ask reads it")

	_, known, read = f.icon("https://example.com")
	assert.False(t, known)
	assert.False(t, read, "it is not read twice at once")

	f.keep("https://example.com", anIcon)

	icon, known, _ := f.icon("https://example.com")
	assert.True(t, known)
	assert.Equal(t, anIcon, icon)

	f.keep("https://none.example", "")

	icon, known, read = f.icon("https://none.example")
	assert.True(t, known, "a site with no icon is not read again")
	assert.False(t, read)
	assert.Empty(t, icon)

	assert.Equal(t, "https://example.com", site("https://example.com/a/b?c"))
	assert.Empty(t, site("about:blank"))
	assert.Empty(t, site("chrome://newtab/"))
}
