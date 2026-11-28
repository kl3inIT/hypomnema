package listening

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
)

// Checking what Muse found (issue #1292, design row "Verification"): each post's link is opened, and the post counts
// only when its page is that post, holds the quote, and its date, read from the link's id or the page and never
// from Muse, lies in the window and agrees with the date Muse gave. last30days checks each post Grok gives against
// the window by the date its id holds, because a fabricated post "carries a plausible handle and a numeric id by
// construction" (grok_x._validate_items, lib/grok_x.py:31-36 and 400-440); trend-opportunity-radar opens each
// detail and keeps it only when its identity is the requested one and its text and time are there
// (run_facebook_opencli_capture.py:236-276, normalize_detail).

const (
	// verifyGap is the gap between two pages opened on one site: last30days' keyless pace, a call a second
	// (lib/http.py:2413-2416), as the Reddit backend's.
	verifyGap = time.Second
	// igEpochMS is the epoch an Instagram media id's top bits count milliseconds from; a Threads post's shortcode is
	// the same id (last30days shortform_oss._IG_EPOCH_MS and _IG_ALPHABET, lib/shortform_oss.py:66-70).
	igEpochMS  = 1314220021721
	igAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	// dateSlack is how far Muse's date may be from the link's: a day each side, for its time zone (last30days
	// shortform_oss._outside_window, "one day of slack on each side covers timezone").
	dateSlack = 1
)

// igFloor: an id dated before 2016 is no post's (last30days shortform_oss._MIN_ID_TS).
var igFloor = time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)

// shortcodeDate is the date an Instagram or Threads shortcode holds: its media id's top bits are milliseconds since
// igEpochMS; nil when it is no shortcode or the date is not plausible, before 2016 or more than two days ahead
// (last30days shortform_oss.post_timestamp_from_url, lib/shortform_oss.py:131-160).
func shortcodeDate(code string, now time.Time) *time.Time {
	if code == "" || len(code) > 11 {
		return nil
	}

	var id uint64

	for _, ch := range code {
		i := strings.IndexRune(igAlphabet, ch)
		if i < 0 {
			return nil
		}

		id = id*64 + uint64(i)
	}

	at := time.UnixMilli(int64(id>>23) + igEpochMS).UTC()
	if at.Before(igFloor) || at.After(now.Add(48*time.Hour)) {
		return nil
	}

	return &at
}

// page is what a post's page shows to a reader who is not signed in: its meta tags, title and first time element.
type page struct {
	final       *url.URL
	ogURL       string
	description string
	metaDesc    string
	ogTitle     string
	title       string
	time        string
	password    bool
}

// unavailable are the words of a page that is no longer there (trend-opportunity-radar is_unavailable_surface,
// run_facebook_opencli_capture.py:85-87).
var unavailable = regexp.MustCompile(
	`(?i)link may be broken|page may have been removed|content isn't available|content is not available`)

// instagramDescription is an Instagram page's description, "12 likes, 3 comments - name on …", which gives the post's
// counts and its account (trend-opportunity-radar run_instagram_opencli_capture.py:55-63); instagramTitle is its
// title, `… on Instagram: "caption"`, which gives the caption (the same lines).
var (
	instagramDescription = regexp.MustCompile(
		`(?i)^([\d,.]+) likes?,\s*([\d,.]+) comments?\s*-\s*([A-Za-z0-9._]+)\s+on\s+`)
	instagramTitle  = regexp.MustCompile(`(?s)on Instagram:\s*"(.*)"\s*$`)
	instagramQuoted = regexp.MustCompile(`(?s):\s*"(.*)"\.?\s*$`)
)

// check opens a post's link and gives it as an item, whether it was verified (an unverified one says why in its
// Check), and the group or account observed for it, from its link or its page, empty when neither names one. Every
// link is opened while the search's Muse time lasts, even one whose id dates it outside the window (issue #1292:
// open each link), and the post counts only when everything checks:
//   - the page is the post, not a sign-in page, a gone post or another post;
//   - the quote is the caption's, found in one field that holds the caption (og:description, the description, or
//     Instagram's title), never stitched from several; a quote Muse read from a comment or an image's text is not in
//     a page a reader who is not signed in sees, so it cannot be checked (trend-opportunity-radar
//     comment-evidence-contract.md:5 and run_facebook_opencli_capture.py:249-271 keep observed comments apart);
//   - Muse gave a date in the form asked, and the date read from the link or the page, never Muse's, lies in the
//     window and within a day of it.
func (mr *museReader) check(ctx context.Context, r Request, lane *museLane, p musePost) (Item, bool, string) {
	it := Item{
		NativeID: p.ref.id, URL: p.ref.url, Author: p.author, Container: p.ref.container, Text: p.quote,
		QuoteFrom: p.quoteFrom, DateConfidence: "low",
	}

	observed := p.ref.container

	if len(p.counts) > 0 {
		it.Engagement = p.counts
	}

	museDate := parseMuseDate(p.date)
	it.PublishedAt = museDate

	var linkDate *time.Time
	if mr.site.dated {
		linkDate = shortcodeDate(p.ref.id, r.now())
	}

	fail := func(format string, args ...any) (Item, bool, string) {
		if it.Container == "" {
			it.Container = p.where
		}

		it.Check = "unverified: " + fmt.Sprintf(format, args...)

		return it, false, observed
	}

	left := lane.left(r.now())
	if left <= 0 {
		return fail("not checked: the search's time ran out")
	}

	pg, err := mr.open(ctx, r, p.ref.url, left)
	if err != nil {
		return fail("not checked: %s", err.Error())
	}

	if why := pg.notThePost(mr.site, p.ref); why != "" {
		return fail("%s", why)
	}

	desc := instagramDescription.FindStringSubmatch(pg.description)
	if observed == "" && desc != nil {
		observed = "@" + desc[3]
	}

	it.Container = observed

	quote := foldText(p.quote)

	switch {
	case p.quoteFrom == "comment", p.quoteFrom == "image_text":
		return fail("not checkable: Muse read the quote from %s, which the page does not show",
			quoteSource(p.quoteFrom))
	case p.quoteFrom == "":
		return fail("Muse did not say where it read the quote")
	case quote == "":
		return fail("Muse gave no quote")
	case !slices.ContainsFunc(pg.captions(mr.site),
		func(field string) bool { return strings.Contains(foldText(field), quote) }):
		return fail("the page does not hold the quote in its caption")
	case museDate == nil:
		return fail("Muse gave no date in the form asked (YYYY-MM-DD): %q", p.date)
	}

	dated, by := linkDate, "link"
	if dated == nil {
		dated, by = parseDate(pg.time), "page"
	}

	switch {
	case dated == nil:
		return fail("its date cannot be read from the link or the page")
	case !inDays(dated, r.From, r.To):
		it.PublishedAt = dated

		return fail("its %s dates it %s, outside the window", by, dated.Format(time.DateOnly))
	case math.Abs(museDate.Sub(dayOf(*dated)).Hours()/24) > dateSlack:
		it.PublishedAt = dated

		return fail("Muse dated it %s; its %s says %s", museDate.Format(time.DateOnly), by,
			dated.Format(time.DateOnly))
	}

	if desc != nil {
		likes, _ := parseCount(desc[1])
		comments, _ := parseCount(desc[2])
		it.Engagement = map[string]float64{"likes": likes, "comments": comments}
	}

	it.PublishedAt = dated
	it.DateConfidence = "high"
	it.Check = fmt.Sprintf("verified: the caption holds the quote; dated %s by its %s", dated.Format(time.DateOnly), by)

	return it, true, observed
}

// captions are the page's fields that hold the post's caption, each on its own. An Instagram page's description and
// title wrap the caption in metadata, its counts, account and date ("12 likes, 3 comments - name on November 4,
// 2026: "caption""), so only the caption inside is taken, as trend-opportunity-radar reads it, the title's first,
// then the description's (run_instagram_opencli_capture.py:55-71); a field it cannot split gives none.
func (pg page) captions(site museSite) []string {
	if site.source != SourceInstagram {
		return []string{pg.description, pg.metaDesc}
	}

	var out []string

	if m := instagramTitle.FindStringSubmatch(pg.ogTitle); m != nil {
		out = append(out, m[1])
	}

	for _, field := range []string{pg.description, pg.metaDesc} {
		if m := instagramQuoted.FindStringSubmatch(field); m != nil {
			out = append(out, m[1])
		}
	}

	return out
}

// quoteSource says where Muse read a quote.
func quoteSource(from string) string {
	switch from {
	case "caption":
		return "the caption"
	case "image_text":
		return "the text in an image or video"
	case "comment":
		return "a comment"
	default:
		return "the post"
	}
}

func dayOf(t time.Time) time.Time {
	t = t.UTC()

	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// notThePost says why a page is not the post asked for, empty when it is: a sign-in page or a page that asks for a
// password (radar detect_safety_stop, run_facebook_opencli_capture.py:90-104), a page that is gone (radar
// is_unavailable_surface), or another post or none (radar normalize_detail compares the requested and observed ids,
// run_facebook_opencli_capture.py:236-240).
func (pg page) notThePost(site museSite, want postRef) string {
	path := strings.ToLower(pg.final.Path)

	switch {
	case strings.HasPrefix(path, "/login"), strings.HasPrefix(path, "/accounts/login"),
		strings.HasPrefix(path, "/checkpoint"), pg.password:
		return "the page asks for a sign-in"
	case unavailable.MatchString(pg.description + "\n" + pg.title):
		return "the post is not available"
	}

	observed := pg.ogURL
	if observed == "" {
		observed = pg.final.String()
	}

	got, ok := site.post(observed)

	switch {
	case !ok:
		return "the page is not a " + site.name + " post"
	case got.id != want.id:
		return "the page is another post (" + got.url + ")"
	}

	return ""
}

// open reads a post's page, in its turn at the site, as a reader who is not signed in: the same request the probe of
// 2026-11-27 made with curl, which Threads answers with the post's caption in og:description.
func (mr *museReader) open(ctx context.Context, r Request, address string, left time.Duration) (page, error) {
	host := hostOf(address)

	// Within the search's Muse time, the wait for the site's turn included.
	ctx, cancel := context.WithTimeout(ctx, left)
	defer cancel()

	if r.Pace != nil {
		done, err := r.Pace(ctx, host, verifyGap)
		if err != nil {
			return page{}, fmt.Errorf("the search's time ran out")
		}

		defer done()
	}

	ctx, cancelCall := context.WithTimeout(ctx, callTimeout)
	defer cancelCall()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return page{}, err
	}

	req.Header.Set("User-Agent", redditUserAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := mr.client.Do(req)
	if err != nil {
		return page{}, fmt.Errorf("%s", AsFailure(err).Detail)
	}

	defer resp.Body.Close() //nolint:errcheck // A read body's close has nothing to report.

	if resp.StatusCode != http.StatusOK {
		return page{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	pg := readPostPage(io.LimitReader(resp.Body, maxBody))
	pg.final = resp.Request.URL

	return pg, nil
}

// readPostPage reads a page's meta tags, its title, its first time element and whether it asks for a password: the
// fields radar reads of a detail (og:url, og:title, og:description or the description, the first time's datetime,
// password inputs: run_facebook_opencli_capture.py:59, run_instagram_opencli_capture.py:23).
func readPostPage(body io.Reader) page {
	var (
		pg      page
		inTitle bool
	)

	z := html.NewTokenizer(body)

	for {
		switch z.Next() {
		case html.ErrorToken:
			return pg
		case html.TextToken:
			if inTitle && pg.title == "" {
				pg.title = strings.TrimSpace(string(z.Text()))
			}
		case html.EndTagToken:
			inTitle = false
		case html.CommentToken, html.DoctypeToken:
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			attr := func(name string) string {
				for _, a := range tok.Attr {
					if strings.EqualFold(a.Key, name) {
						return a.Val
					}
				}

				return ""
			}

			switch tok.Data {
			case "title":
				inTitle = true
			case "time":
				if pg.time == "" {
					pg.time = attr("datetime")
				}
			case "input":
				if strings.EqualFold(attr("type"), "password") {
					pg.password = true
				}
			case "meta":
				content := attr("content")

				switch strings.ToLower(attr("property") + attr("name")) {
				case "og:url":
					pg.ogURL = content
				case "og:description":
					pg.description = content
				case "description":
					pg.metaDesc = content
				case "og:title":
					pg.ogTitle = content
				}
			}
		}
	}
}

// foldText is text as a quote is compared: NFKC, lowercased, typographic quotation marks made plain, every run of
// white space one space, and no punctuation or space at either end, so that a quote Muse ends with a comma the page
// does not have still matches (probe 2026-11-27: "Chào mừng em về vs đội của a," against the page's
// "Chào mừng em về vs đội của a"). No reference compares a model's quote to a page; this is the issue's own check.
func foldText(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	s = strings.NewReplacer("“", `"`, "”", `"`, "„", `"`, "‘", "'", "’", "'", "«", `"`, "»", `"`).Replace(s)
	s = strings.Join(strings.Fields(s), " ")

	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
}

var (
	isoDay = regexp.MustCompile(`\b(\d{4})-(\d{1,2})-(\d{1,2})\b`)
	vnDay  = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`)
)

// parseMuseDate reads the date Muse gave: YYYY-MM-DD as asked, or D/M/YYYY as it wrote in Vietnamese (probe
// 2026-11-27, "31/10/2026"); nil when none.
func parseMuseDate(s string) *time.Time {
	var y, m, d string

	if g := isoDay.FindStringSubmatch(s); g != nil {
		y, m, d = g[1], g[2], g[3]
	} else if g := vnDay.FindStringSubmatch(s); g != nil {
		y, m, d = g[3], g[2], g[1]
	} else {
		return nil
	}

	yi, _ := strconv.Atoi(y)
	mi, _ := strconv.Atoi(m)
	di, _ := strconv.Atoi(d)

	t := time.Date(yi, time.Month(mi), di, 0, 0, 0, 0, time.UTC)

	// A day that does not exist (2026-11-31) is no date: time.Date would roll it into the next month.
	if t.Year() != yi || int(t.Month()) != mi || t.Day() != di {
		return nil
	}

	return &t
}
