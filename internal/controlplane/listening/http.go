package listening

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// One call to a source, as every backend makes it (issue #1289): in its turn at the host (Request.Pace), its path
// checked for a rest once the turn came and rested after a failure that rests (Request.Resting and Rest), the status
// read as a code (HTTPFailure), the answer bounded (maxBody). The Reddit backends of #1287 made their calls this way;
// the keyless sources of #1289 share it.

const (
	// maxRedirects is how many redirects a page read follows by hand: the web fetch's (websearch.maxRedirects,
	// OmniRoute's MAX_SEARCH_REDIRECTS).
	maxRedirects = 5
	// pageUserAgent names this control plane to a site it reads a page of, in the form harken's sources send
	// (harken sources/base.py USER_AGENT, "harken/0.1 (+url)"), so a site's robots.txt can name it (robots.go).
	pageUserAgent = "hypomnema/1 (+https://github.com/kl3inIT/hypomnema)"
)

// call is one HTTP request to a source.
type call struct {
	// host is the turn it waits for and gap the least time after the host's last call (Pacer.Wait); path names the
	// endpoint for its rest (Request.Resting); empty: not rested apart.
	host string
	gap  time.Duration
	path string
	// method is GET when empty; body is sent with contentType.
	method      string
	address     string
	body        []byte
	contentType string
	accept      string
	userAgent   string
	// header is sent as well, after the others (a browser's headers, a cookie).
	header http.Header
	// json: a page where JSON was asked is a refusal, a challenge or a sign-in page (last30days reddit_public:
	// "Anti-bot HTML response").
	json bool
	// slowDown reads a 422 as Arctic Shift's "slow down", a rate limit (last30days reddit_arctic._is_slow_down).
	slowDown bool
	// failure reads a status that is not 200 as the source means it; nil: HTTPFailure.
	failure func(status int, header http.Header, now time.Time) *SourceError
	// follow follows redirects by hand, for a client that does not (a page client, websearch.PageClient), each hop
	// dialed through the client's address check.
	follow bool
}

// answer is what a call read.
type answer struct {
	body   []byte
	header http.Header
	// url is where the answer came from, after redirects.
	url string
}

// do makes the call in its turn: a failure that rests rests its path, so another search queued behind this one is
// not sent to a path that just refused.
func (c call) do(ctx context.Context, client *http.Client, r Request) (answer, error) {
	if r.Pace != nil {
		done, err := r.Pace(ctx, c.host, c.gap)
		if err != nil {
			return answer{}, AsFailure(err)
		}

		defer done()
	}

	if r.Resting != nil && c.path != "" {
		if rs := r.Resting(c.path); rs != nil {
			return answer{}, &SourceError{Code: rs.Code, Detail: rs.Detail, Until: rs.Until, resting: true}
		}
	}

	a, err := c.once(ctx, client, r)
	if err != nil && r.Rest != nil && c.path != "" {
		f := AsFailure(err)
		if until := r.Rest(c.path, f); !until.IsZero() {
			f.Until = until
		}

		return answer{}, f
	}

	return a, err
}

// once is the request and its redirects, without a turn.
func (c call) once(ctx context.Context, client *http.Client, r Request) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	address := c.address

	for hop := 0; ; hop++ {
		resp, err := c.send(ctx, client, address)
		if err != nil {
			return answer{}, err
		}

		if location := resp.Header.Get("Location"); c.follow && resp.StatusCode >= 300 && resp.StatusCode < 400 &&
			location != "" {
			_ = resp.Body.Close()

			next, err := url.Parse(address)
			if err != nil {
				return answer{}, Fail(CodeBroken, "%s", err.Error())
			}

			to, err := next.Parse(location)
			if err != nil || (to.Scheme != "http" && to.Scheme != "https") || hop+1 > maxRedirects {
				return answer{}, Fail(CodeBroken, "the source redirected where it cannot be followed")
			}

			address = to.String()

			continue
		}

		defer resp.Body.Close() //nolint:errcheck // A read body's close has nothing to report.

		return c.read(resp, address, r)
	}
}

func (c call) send(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	method := c.method
	if method == "" {
		method = http.MethodGet
	}

	var body io.Reader
	if c.body != nil {
		body = bytes.NewReader(c.body)
	}

	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, Fail(CodeBroken, "%s", err.Error())
	}

	ua := c.userAgent
	if ua == "" {
		ua = redditUserAgent
	}

	req.Header.Set("User-Agent", ua)

	switch {
	case c.accept != "":
		req.Header.Set("Accept", c.accept)
	case c.json:
		req.Header.Set("Accept", "application/json")
	}

	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}

	for k, vs := range c.header {
		req.Header[k] = vs
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, AsFailure(err)
	}

	return resp, nil
}

func (c call) read(resp *http.Response, address string, r Request) (answer, error) {
	if resp.StatusCode == http.StatusUnprocessableEntity && c.slowDown {
		f := HTTPFailure(http.StatusTooManyRequests, resp.Header, r.now())
		f.Detail = "HTTP 422 slow down"

		return answer{}, f
	}

	if resp.StatusCode != http.StatusOK {
		if c.failure != nil {
			return answer{}, c.failure(resp.StatusCode, resp.Header, r.now())
		}

		return answer{}, HTTPFailure(resp.StatusCode, resp.Header, r.now())
	}

	if c.json {
		// Apple's search answers its JSON as text/javascript (itunes.apple.com, read 2026-11-28).
		if media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); !strings.Contains(media, "json") &&
			media != "text/javascript" {
			return answer{}, Fail(CodeRefused, "the source answered a page (%s), not JSON: a challenge or a sign-in page",
				media)
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return answer{}, AsFailure(err)
	}

	final := address
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}

	return answer{body: body, header: resp.Header, url: final}, nil
}

// getJSON reads one JSON answer from a source in its turn at the host (call.do).
func getJSON(ctx context.Context, client *http.Client, r Request, host string, gap time.Duration, slowDown bool,
	path, address string, out any,
) error {
	return callJSON(ctx, client, r, call{host: host, gap: gap, path: path, address: address, json: true,
		slowDown: slowDown}, out)
}

// callJSON makes a call and decodes its JSON answer; an answer that is not the source's JSON is broken.
func callJSON(ctx context.Context, client *http.Client, r Request, c call, out any) error {
	c.json = true

	a, err := c.do(ctx, client, r)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(a.body, out); err != nil {
		return Fail(CodeBroken, "the answer is not the source's JSON: %s", err.Error())
	}

	return nil
}
