package websearch_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

const article = `<!doctype html><html><head><title>Release notes | Example</title></head><body>
<nav><a href="/">Home</a> <a href="/blog">Blog</a></nav>
<article><h1>Release notes</h1>
<p>Go 1.27 brings a faster garbage collector, which lowers the pauses that services with large heaps see under load.
The change needs no new setting and works on every platform the release supports.</p>
<p>The standard library gains an iterator over the lines of a reader, and <a href="/doc">the documentation</a>
says how to move code that splits text by hand to it, with examples for files and network streams.</p>
<p>Tools: the vet command reports more mistakes in format strings, and the test command can shuffle test order.</p>
</article><footer>© Example</footer></body></html>`

// site serves pages for the tests on the loopback address, which the
// fetcher may reach only because the test's rules list its port.
func site(t *testing.T, handler http.HandlerFunc) (*httptest.Server, egress.Allow) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return srv, allowOf(t, srv)
}

func allowOf(t *testing.T, servers ...*httptest.Server) egress.Allow {
	t.Helper()

	entries := make([]string, 0, len(servers))
	for _, srv := range servers {
		u, err := url.Parse(srv.URL)
		require.NoError(t, err)

		entries = append(entries, u.Host)
	}

	allow, err := egress.ParseAllow(entries)
	require.NoError(t, err)

	return allow
}

func fetcher(allow egress.Allow, jina string) *websearch.Service {
	return websearch.New(websearch.Config{Rules: egress.Rules{Allow: allow}, JinaURL: jina})
}

// Scenario: a server that offers markdown is asked for it first and its
// markdown is kept as it is; an HTML page's readable part is made markdown,
// without its menu and footer, its links made whole, with its title; JSON
// is indented.
func TestAPageIsReadAsMarkdown(t *testing.T) {
	t.Parallel()

	var accept string

	srv, allow := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notes.md":
			accept = r.Header.Get("Accept")
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = io.WriteString(w, "# Notes\n\nKept as written.")
		case "/data":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"a":1}`)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, article)
		}
	})
	svc := fetcher(allow, "")

	page, err := svc.Fetch(t.Context(), srv.URL+"/notes.md")
	require.NoError(t, err)
	assert.Equal(t, "# Notes\n\nKept as written.", page.Content)
	assert.Equal(t, "text/markdown, text/html;q=0.9, */*;q=0.1", accept)
	assert.False(t, page.Truncated)

	page, err = svc.Fetch(t.Context(), srv.URL+"/blog/go")
	require.NoError(t, err)
	assert.Equal(t, srv.URL+"/blog/go", page.URL)
	assert.Contains(t, page.Title, "Release notes")
	assert.Contains(t, page.Content, "Go 1.27 brings a faster garbage collector")
	assert.Contains(t, page.Content, "[the documentation]("+srv.URL+"/doc)")
	assert.NotContains(t, page.Content, "© Example")

	page, err = svc.Fetch(t.Context(), srv.URL+"/data")
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"a\": 1\n}", page.Content)

	text := page.Text()
	assert.Contains(t, text, "Page "+srv.URL+"/data")
	assert.Contains(t, text, "Source: Web Fetch")
}

// A page longer than 20,000 characters is cut there and says so.
func TestALongPageIsCutAtTwentyThousandCharacters(t *testing.T) {
	t.Parallel()

	srv, allow := site(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = io.WriteString(w, strings.Repeat("é", websearch.MaxContent+10))
	})

	page, err := fetcher(allow, "").Fetch(t.Context(), srv.URL)
	require.NoError(t, err)
	assert.Len(t, []rune(page.Content), websearch.MaxContent)
	assert.True(t, page.Truncated)
	assert.Contains(t, page.Text(), "The page was cut at 20000 characters.")
}

// Scenario: redirects are followed by hand, a credential in the address
// does not cross to another origin, and a sixth redirect is refused. A
// redirect to an address the rules do not allow is refused before anything
// is sent there, and so is such a page asked for at once: loopback, the
// local network and the cloud's metadata service alike.
func TestRedirectsAreFollowedAndPrivateAddressesRefused(t *testing.T) {
	t.Parallel()

	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secret")
	}))
	t.Cleanup(private.Close)

	var lastAuth string

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = io.WriteString(w, "arrived")
	}))
	t.Cleanup(other.Close)

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/hop":
			http.Redirect(w, r, srv.URL+"/hop2", http.StatusFound)
		case r.URL.Path == "/hop2":
			http.Redirect(w, r, other.URL+"/end", http.StatusMovedPermanently)
		case r.URL.Path == "/loop":
			http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
		case r.URL.Path == "/inside":
			http.Redirect(w, r, private.URL, http.StatusFound)
		case r.URL.Path == "/file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)

	svc := fetcher(allowOf(t, srv, other), "")

	withUser, err := url.Parse(srv.URL + "/hop")
	require.NoError(t, err)

	withUser.User = url.UserPassword("me", "pw")

	page, err := svc.Fetch(t.Context(), withUser.String())
	require.NoError(t, err)
	assert.Equal(t, other.URL+"/end", page.URL)
	assert.Equal(t, "arrived", page.Content)
	assert.Empty(t, lastAuth, "the address's credential does not cross to another origin")

	_, err = svc.Fetch(t.Context(), srv.URL+"/loop")
	require.ErrorIs(t, err, websearch.ErrUnreadable)
	assert.Contains(t, err.Error(), "more than 5 redirects")

	_, err = svc.Fetch(t.Context(), srv.URL+"/inside")
	require.ErrorIs(t, err, websearch.ErrRefused)
	assert.Contains(t, err.Error(), "a service on the browser's own machine")

	_, err = svc.Fetch(t.Context(), srv.URL+"/file")
	require.ErrorIs(t, err, websearch.ErrRefused)

	public := fetcher(egress.Allow{}, "")

	for _, address := range []string{private.URL, "http://169.254.169.254/latest/meta-data/", "http://10.0.0.5/"} {
		_, err := public.Fetch(t.Context(), address)
		require.ErrorIs(t, err, websearch.ErrRefused, address)
	}

	_, err = public.Fetch(t.Context(), "ftp://example.com/")
	require.ErrorIs(t, err, websearch.ErrInvalid)
}

// Scenario: a page whose HTML holds nothing readable is read by Jina
// Reader, asked for markdown without its cache; a page Jina cannot read
// either fails, and says both.
func TestAPageWithNothingReadableIsReadByJina(t *testing.T) {
	t.Parallel()

	var asked *http.Request

	jinaText := strings.Repeat("Rendered by a browser. ", 10)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r

		if strings.Contains(r.URL.Path, "/broken") {
			_, _ = io.WriteString(w, "Title: x\n\nWarning: Target URL returned error 500\n\nMarkdown Content:\nnothing")

			return
		}

		_, _ = fmt.Fprintf(w, "Title: App\n\nURL Source: x\n\nMarkdown Content:\n%s", jinaText)
	}))
	t.Cleanup(jina.Close)

	srv, allow := site(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><script src="app.js"></script></head><body><div id="root"></div></body></html>`)
	})
	svc := fetcher(allow, jina.URL)

	page, err := svc.Fetch(t.Context(), srv.URL+"/app")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(jinaText), page.Content)
	assert.Equal(t, "/"+srv.URL+"/app", asked.URL.Path)
	assert.Equal(t, "text/markdown", asked.Header.Get("Accept"))
	assert.Equal(t, "true", asked.Header.Get("X-No-Cache"))

	_, err = svc.Fetch(t.Context(), srv.URL+"/broken")
	require.ErrorIs(t, err, websearch.ErrUnreadable)
	assert.Contains(t, err.Error(), "Jina Reader could not read it either")
}

// Scenario (issue #667): a site behind a bot wall answers 403 to the
// direct request, and Jina Reader, fetching from its own servers, reads it. A
// wall Jina meets too, or a bot check it copies, fails and says both; a page
// that does not exist (404, 410) and an address the check refuses are never
// sent to Jina.
func TestAPageThatAnswersOutside2xxIsReadByJina(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex

	asked := []string{}
	jinaText := strings.Repeat("A model guide for the GPT-6 family. ", 10)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/walled"):
			_, _ = io.WriteString(w, "Title: x\n\nWarning: Target URL returned error 403: Forbidden\n\nMarkdown Content:\nx")
		case strings.HasSuffix(r.URL.Path, "/check"):
			_, _ = io.WriteString(w, "Title: Just a moment\n\nMarkdown Content:\nVerify you are human by completing "+
				"the action below. example.com needs to review the security of your connection before proceeding.")
		default:
			_, _ = fmt.Fprintf(w, "Title: Guide\n\nURL Source: x\n\nMarkdown Content:\n%s", jinaText)
		}
	}))
	t.Cleanup(jina.Close)

	srv, allow := site(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cf-Mitigated", "challenge")

		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/removed":
			w.WriteHeader(http.StatusGone)
		default:
			w.WriteHeader(http.StatusForbidden)
		}

		_, _ = io.WriteString(w, "<html><body>Just a moment...</body></html>")
	})
	svc := fetcher(allow, jina.URL)

	page, err := svc.Fetch(t.Context(), srv.URL+"/guide")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(jinaText), page.Content)
	assert.Equal(t, srv.URL+"/guide", page.URL)

	for _, path := range []string{"/walled", "/check"} {
		_, err = svc.Fetch(t.Context(), srv.URL+path)
		require.ErrorIs(t, err, websearch.ErrUnreadable, path)
		assert.Contains(t, err.Error(), "it answered 403, and Jina Reader could not read it either", path)
	}

	for _, path := range []string{"/missing", "/removed"} {
		_, err = svc.Fetch(t.Context(), srv.URL+path)
		require.ErrorIs(t, err, websearch.ErrUnreadable, path)
	}

	_, err = fetcher(egress.Allow{}, jina.URL).Fetch(t.Context(), srv.URL+"/guide")
	require.ErrorIs(t, err, websearch.ErrRefused)

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, []string{"/" + srv.URL + "/guide", "/" + srv.URL + "/walled", "/" + srv.URL + "/check"}, asked,
		"Jina is asked only for the walled pages, never for a missing page or a refused address")
}

// Scenario (issue #1306): a page says when it was published in its own metadata, which Readability reads
// (article:published_time here); a page Jina Reader reads says it in its "Published Time:" line, as Agent-Reach reads
// it (agent_reach/channels/web.py:141-150); a page that says nothing has no date, never the time it was read.
func TestAPageSaysWhenItWasPublished(t *testing.T) {
	t.Parallel()

	jinaText := strings.Repeat("Vmap ra mắt bản đồ số cho người Việt. ", 10)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "Title: Vmap\n\nURL Source: x\n\nPublished Time: 2019-03-02T08:30:00+07:00\n\n"+
			"Markdown Content:\n%s", jinaText)
	}))
	t.Cleanup(jina.Close)

	srv, allow := site(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")

		switch r.URL.Path {
		case "/dated":
			_, _ = io.WriteString(w, strings.Replace(article, "<title>",
				`<meta property="article:published_time" content="2019-03-02T10:00:00Z"><title>`, 1))
		case "/app":
			_, _ = io.WriteString(w, `<html><body><div id="root"></div></body></html>`)
		default:
			_, _ = io.WriteString(w, article)
		}
	})
	svc := fetcher(allow, jina.URL)

	page, err := svc.Fetch(t.Context(), srv.URL+"/dated")
	require.NoError(t, err)
	require.NotNil(t, page.Published)
	assert.Equal(t, time.Date(2019, 3, 2, 10, 0, 0, 0, time.UTC), *page.Published)

	page, err = svc.Fetch(t.Context(), srv.URL+"/app")
	require.NoError(t, err)
	require.NotNil(t, page.Published, "Jina's Published Time line")
	assert.Equal(t, time.Date(2019, 3, 2, 1, 30, 0, 0, time.UTC), *page.Published)

	page, err = svc.Fetch(t.Context(), srv.URL+"/undated")
	require.NoError(t, err)
	assert.Nil(t, page.Published)
}
