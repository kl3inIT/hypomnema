package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/itlightning/dateparse"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
)

// Reading a page follows OpenClaw's web_fetch (src/agents/tools/web-fetch.ts):
// markdown asked for first, the readable part of HTML taken with Readability
// (extensions/web-readability; here go-readability, a port of the same
// Mozilla code) and made markdown, JSON indented, anything else as it came,
// and the text cut at 20,000 characters. When Readability finds nothing, or
// the server answers outside 2xx (a bot wall's 403, issue #667), Jina Reader
// is asked without a key, as omp's fetch falls back to it
// (src/tools/fetch.ts) and OpenClaw asks its provider on a response that is
// not ok.

const (
	// MaxContent is OpenClaw's DEFAULT_FETCH_MAX_CHARS.
	MaxContent = 20_000
	// maxPageBytes is OpenClaw's DEFAULT_FETCH_MAX_RESPONSE_BYTES; a page
	// past it is read that far and marked truncated.
	maxPageBytes = 750_000
	// fetchTimeout bounds reading a page, its redirects included
	// (OpenClaw's DEFAULT_TIMEOUT_SECONDS).
	fetchTimeout = 30 * time.Second
	// maxRedirects is OmniRoute's MAX_SEARCH_REDIRECTS.
	maxRedirects = 5
	// jinaTimeout and maxJinaBytes are omp's REMOTE_READER_MAX_MS and
	// JINA_READER_MAX_BYTES.
	jinaTimeout  = 10 * time.Second
	maxJinaBytes = 2 << 20
	// pageAccept and pageUserAgent are what OpenClaw sends.
	pageAccept    = "text/markdown, text/html;q=0.9, */*;q=0.1"
	pageLanguage  = "en-US,en;q=0.9"
	pageUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_7_2) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/122.0.0.0 Safari/537.36"
)

// refusedError is a connection the address check refused.
type refusedError struct {
	address netip.AddrPort
	reason  egress.Refusal
}

func (e *refusedError) Error() string {
	return fmt.Sprintf("%s is %s", e.address, e.reason)
}

// pageClient reads pages through the shared browser's address check, made
// on the address each connection is about to use, after the name was
// resolved, as OmniRoute's connect-time guard and the browser's gate do: a
// name that resolves to a private address, or a redirect to one, reaches
// nothing. No proxy is used, so the address checked is the one connected to.
// Redirects are not followed by the client: Fetch follows them.
func pageClient(rules egress.Rules) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			to, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrRefused, address)
			}

			if reason := rules.Refusal(to.Port(), []netip.Addr{to.Addr()}, egress.OwnAddresses()); reason != egress.Allowed {
				return &refusedError{address: to, reason: reason}
			}

			return nil
		},
	}

	//nolint:forcetypeassert // the standard library's own type
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext

	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Fetch reads a page as markdown.
func (s *Service) Fetch(ctx context.Context, address string) (Page, error) {
	target, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return Page{}, fmt.Errorf("%w: a page is an http or https address", ErrInvalid)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	resp, final, err := s.get(ctx, target)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return s.refused(ctx, final, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return Page{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}

	cut := len(body) > maxPageBytes
	if cut {
		body = body[:maxPageBytes]
	}

	page := Page{URL: final.String(), Truncated: cut}

	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if kind == "" && looksLikeHTML(body) {
		kind = "text/html"
	}

	switch {
	case kind == "text/markdown":
		page.Content = string(body)
	case kind == "text/html" || kind == "application/xhtml+xml":
		page.Title, page.Content, page.Published, err = readable(body, final)
		if err != nil {
			// Readability found nothing to read: Jina Reader reads the page as a browser would (omp).
			text, published, jinaErr := s.jina(ctx, final)
			if jinaErr != nil {
				return Page{}, fmt.Errorf("%w: its readable part could not be found (%w), and Jina Reader "+
					"could not read it either (%w)", ErrUnreadable, err, jinaErr)
			}

			page.Title, page.Content, page.Published = "", text, published
		}
	case kind == "application/json" || strings.HasSuffix(kind, "+json"):
		var indented bytes.Buffer
		if json.Indent(&indented, body, "", "  ") == nil {
			page.Content = indented.String()
		} else {
			page.Content = string(body)
		}
	default:
		page.Content = string(body)
	}

	if runes := []rune(page.Content); len(runes) > MaxContent {
		page.Content, page.Truncated = string(runes[:MaxContent]), true
	}

	return page, nil
}

// refused reads a page whose server answered outside 2xx through Jina Reader,
// which fetches it from its own servers, as OpenClaw asks its fetch provider
// when the response is not ok (src/agents/tools/web-fetch.ts, !res.ok) and
// omp's remote readers get past a wall that refuses this host
// (src/tools/fetch.ts, REMOTE_FETCH_PROVIDERS). A page that does not exist
// (404, 410) is not asked for again (inferred: neither reference skips it).
// A request the address check refused never gets here: get returns it as
// ErrRefused, and Jina is not asked for a private address (OpenClaw rethrows
// SsrFBlockedError).
func (s *Service) refused(ctx context.Context, page *url.URL, status int) (Page, error) {
	if status == http.StatusNotFound || status == http.StatusGone {
		return Page{}, fmt.Errorf("%w: it answered %d", ErrUnreadable, status)
	}

	text, published, err := s.jina(ctx, page)
	if err != nil {
		return Page{}, fmt.Errorf("%w: it answered %d, and Jina Reader could not read it either (%w)", ErrUnreadable,
			status, err)
	}

	out := Page{URL: page.String(), Content: text, Published: published}
	if runes := []rune(out.Content); len(runes) > MaxContent {
		out.Content, out.Truncated = string(runes[:MaxContent]), true
	}

	return out, nil
}

// get asks for a page and follows its redirects by hand, at most
// maxRedirects, as OmniRoute's fetchWithCheckedRedirects: each hop's address
// is checked as it connects, a hop to another scheme than http(s) is
// refused, and credentials do not cross to another origin.
func (s *Service) get(ctx context.Context, target *url.URL) (*http.Response, *url.URL, error) {
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}

		req.Header.Set("Accept", pageAccept)
		req.Header.Set("Accept-Language", pageLanguage)
		req.Header.Set("User-Agent", pageUserAgent)

		resp, err := s.pages.Do(req)
		if err != nil {
			var refused *refusedError
			if errors.As(err, &refused) {
				return nil, nil, fmt.Errorf("%w: %s", ErrRefused, refused.Error())
			}

			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				err = urlErr.Err
			}

			return nil, nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
		}

		location := resp.Header.Get("Location")
		if !redirects(resp.StatusCode) || location == "" {
			return resp, target, nil
		}

		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()

		if hop >= maxRedirects {
			return nil, nil, fmt.Errorf("%w: more than %d redirects", ErrUnreadable, maxRedirects)
		}

		next, err := target.Parse(location)
		if err != nil || (next.Scheme != "http" && next.Scheme != "https") {
			return nil, nil, fmt.Errorf("%w: it redirects to %q", ErrRefused, location)
		}

		target = nextHop(target, next)
	}
}

// redirects reports the statuses fetch() follows (OmniRoute's REDIRECT_STATUSES).
func redirects(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		return true
	}

	return false
}

// nextHop is the next hop's address. A request here carries no credential
// but the address's own user and password, which Go sends as Basic
// authorization: they are dropped when the hop goes to another origin, as
// OmniRoute drops Authorization (nextHopInit).
func nextHop(from, to *url.URL) *url.URL {
	next := *to
	if from.Scheme != to.Scheme || from.Host != to.Host {
		next.User = nil
	}

	return &next
}

func looksLikeHTML(body []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))

	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// readable is a page's title, its readable part as markdown and when its metadata says it was published.
func readable(body []byte, page *url.URL) (string, string, *time.Time, error) {
	article, err := readability.FromReader(bytes.NewReader(body), page)
	if err != nil {
		return "", "", nil, err
	}

	if article.Node == nil {
		return "", "", nil, errors.New("nothing readable")
	}

	markdown, err := htmltomarkdown.ConvertNode(article.Node, converter.WithDomain(page.String()))
	if err != nil {
		return "", "", nil, err
	}

	text := strings.TrimSpace(string(markdown))
	if text == "" {
		return "", "", nil, errors.New("nothing readable")
	}

	var published *time.Time
	if t, err := article.PublishedTime(); err == nil && !t.IsZero() {
		published = new(t.UTC())
	}

	return strings.TrimSpace(article.Title()), text, published, nil
}

// jinaPublished is Jina Reader's "Published Time:" line before its content, as Agent-Reach reads it
// (agent_reach/channels/web.py:141-150), parsed as Readability parses a page's date (dateparse.ParseAny).
var jinaPublished = regexp.MustCompile(`(?m)^Published Time:\s*(.+)$`)

func publishedIn(head string) *time.Time {
	m := jinaPublished.FindStringSubmatch(head)
	if m == nil {
		return nil
	}

	t, err := dateparse.ParseAny(strings.TrimSpace(m[1]))
	if err != nil || t.IsZero() {
		return nil
	}

	return new(t.UTC())
}

// jina reads a page through Jina Reader without a key, as omp does: markdown
// asked for, the cache skipped, and the text after "Markdown Content:" kept
// unless Jina's own fetch failed or found too little (parseJinaReaderContent).
func (s *Service) jina(ctx context.Context, page *url.URL) (string, *time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, jinaTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.cfg.JinaURL, "/")+"/"+page.String(),
		nil)
	if err != nil {
		return "", nil, err
	}

	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("X-No-Cache", "true")

	raw, err := s.send(req, maxJinaBytes)
	if err != nil {
		return "", nil, err
	}

	const marker = "Markdown Content:"

	text := string(raw)

	head, content, ok := strings.Cut(text, marker)
	if !ok {
		return "", nil, errors.New("its answer has no content")
	}

	for line := range strings.SplitSeq(head, "\n") {
		if strings.HasPrefix(line, "Warning: Target URL returned error") {
			return "", nil, errors.New(strings.TrimSpace(strings.TrimPrefix(line, "Warning: ")))
		}
	}

	content = strings.TrimSpace(content)
	if len(content) < 100 || strings.HasPrefix(content, "Loading...") ||
		strings.HasPrefix(content, "Please enable JavaScript") {
		return "", nil, errors.New("it found too little to read")
	}

	if blockPage(content) {
		return "", nil, errors.New("it was served a bot check instead of the page")
	}

	return content, publishedIn(head), nil
}

// blockPagePhrases and blockPageMaxChars are omp's BLOCK_PAGE_PHRASES and
// BLOCK_PAGE_MAX_CHARS (src/tools/fetch.ts): a remote reader's short copy of
// a bot wall or a JavaScript gate is no rendering of the page.
var blockPagePhrases = []string{
	"blocked by network security", "verify you are human", "are you a robot", "access to this page has been denied",
	"enable javascript", "javascript required", "javascript is required", "turn on javascript",
	"please enable javascript", "browser not supported",
}

const blockPageMaxChars = 2048

// blockPage reports whether a reader's text is a short page holding a bot wall's or a gate's wording.
func blockPage(content string) bool {
	if len(content) > blockPageMaxChars {
		return false
	}

	lower := strings.ToLower(content)
	for _, phrase := range blockPagePhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}

	return false
}
