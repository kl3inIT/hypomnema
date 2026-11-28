package listening

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The app stores of issue #1289: an app's reviews, the app named by the subquery, as an id or by its name. Google Play
// through the call google-play-scraper makes (features/reviews.py, constants/request.py, constants/element.py), the
// App Store through its public reviews feed (obsei sources/appstore.py, marketingskills
// reverse-engineer-marketing/scripts/teardown.mjs). Both read Vietnam's store in Vietnamese, newest first (issue
// #1289's design).

const (
	playURL   = "https://play.google.com"
	itunesURL = "https://itunes.apple.com"
	// storeLang and storeCountry are the language and country the stores are read in (issue #1289: lang=vi,
	// country=vn).
	storeLang    = "vi"
	storeCountry = "vn"
	// playNewest is google-play-scraper's Sort.NEWEST (constants/google_play.py).
	playNewest = 2
	// playMaxCount is the most reviews one call asks (google-play-scraper reviews.MAX_COUNT_EACH_FETCH).
	playMaxCount = 4500
	// appStorePages is the most pages of 50 reviews the feed gives (obsei appstore.MAX_PAGES, marketingskills: "the
	// feed stops at 500").
	appStorePages = 10
	// appStoreGap is the pause between two pages of the feed (marketingskills teardown.mjs cmdReviews, sleep(300)).
	appStoreGap = 300 * time.Millisecond
	// appStoreSearch is how many apps Apple's search is asked; the first is read (marketingskills teardown.mjs:
	// limit 5, results[0]).
	appStoreSearch = 5
)

var (
	// playPackage is an Android package name (obsei sources/playstore.py PlayStoreConfig.package_name).
	playPackage = regexp.MustCompile(`^[A-Za-z]\w*(\.[A-Za-z]\w*)+$`)
	// playDetails is an app's link on Play's search page, the first being its top result.
	playDetails = regexp.MustCompile(`/store/apps/details\?id=([A-Za-z]\w*(?:\.[A-Za-z]\w*)+)`)
)

// playBackend reads an app's newest Google Play reviews, in Vietnamese for Vietnam: the app the query names by its
// package, else the top app of Play's search for it (google-play-scraper features/search.py reads the same page; its
// first app link is taken here rather than its script data's positions), then google-play-scraper's batchexecute call
// page after page until the stream's limit, the last page or a review older than the window. Departing from
// google-play-scraper on what its issues and the research note found wrong: an error on any page fails the stream
// (reviews.py:113-125 ends the data silently), Play's gateway error is a rate limit that rests rather than a sleep and
// a retry (utils/request.py:213-217), TLS is verified (request.py:184 turns it off), and `at` is Play's epoch in UTC
// (element.py:185 makes it naive local time).
func playBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.PlayURL)

	return Backend{
		Name: BackendPlay, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			app := strings.TrimSpace(r.Query)
			if !playPackage.MatchString(app) {
				v := url.Values{"q": {app}, "c": {"apps"}, "hl": {storeLang}, "gl": {storeCountry}}

				a, err := call{
					host: host, path: host + "/store/search", address: e.PlayURL + "/store/search?" + v.Encode(),
					accept: "text/html",
				}.do(ctx, client, r)
				if err != nil {
					return Result{}, err
				}

				m := playDetails.FindStringSubmatch(string(a.body))
				if m == nil {
					return Result{}, Fail(CodeNotFound, "no app on Google Play for %q", app)
				}

				app = m[1]
			}

			address := e.PlayURL + "/_/PlayStoreUi/data/batchexecute?" +
				url.Values{"hl": {storeLang}, "gl": {storeCountry}}.Encode()
			from := dayStart(r.From)

			var (
				items []Item
				token string
				pages int
			)

			for len(items) < r.Limit {
				a, err := call{
					host: host, path: host + "/_/PlayStoreUi/data/batchexecute", method: http.MethodPost, address: address,
					body:        playBody(app, min(r.Limit-len(items), playMaxCount), token),
					contentType: "application/x-www-form-urlencoded",
				}.do(ctx, client, r)
				if err != nil {
					return Result{}, err
				}

				if strings.Contains(string(a.body), "com.google.play.gateway.proto.PlayGatewayError") {
					return Result{}, Fail(CodeRateLimited, "Google Play's gateway asked to slow down")
				}

				reviews, next, err := playReviews(a.body)
				if err != nil {
					return Result{}, err
				}

				pages++
				older := false

				for _, rv := range reviews {
					it := rv.item(app)
					if it.PublishedAt != nil && it.PublishedAt.Before(from) {
						older = true
					}

					items = append(items, it)
				}

				if next == "" || older || len(reviews) == 0 {
					break
				}

				token = next
			}

			return Result{Items: items, Detail: fmt.Sprintf("%s, %d pages", app, pages)}, nil
		},
	}
}

// playBody is google-play-scraper's request for a page of reviews, newest first, with no score or device filter
// (constants/request.py PAYLOAD_FORMAT_FOR_FIRST_PAGE and _FOR_PAGINATED_PAGE).
func playBody(app string, count int, token string) []byte {
	page := strconv.Itoa(count)
	if token != "" {
		quoted, _ := json.Marshal(token)
		page += ",null," + string(quoted)
	}

	quotedApp, _ := json.Marshal(app)
	inner := fmt.Sprintf(`[null,[2,%d,[%s],null,[null,null,null,null,null,null,null,null,null]],[%s,7]]`, playNewest,
		page, quotedApp)
	quotedInner, _ := json.Marshal(inner)

	return []byte(url.Values{"f.req": {`[[["oCPfdb",` + string(quotedInner) + `,null,"generic"]]]`}}.Encode())
}

// playReview is a review as Play lays it out (google-play-scraper constants/element.py:177-189).
type playReview []json.RawMessage

func (rv playReview) at(i int, out any) bool {
	return i < len(rv) && json.Unmarshal(rv[i], out) == nil
}

func (rv playReview) item(app string) Item {
	var (
		id, text string
		user     []json.RawMessage
		name     string
		score    float64
		at       []float64
		thumbs   float64
	)

	rv.at(0, &id)
	rv.at(4, &text)

	if rv.at(1, &user) && len(user) > 0 {
		_ = json.Unmarshal(user[0], &name)
	}

	it := Item{
		NativeID: id, Author: name, Text: text, Container: app,
		URL: playURL + "/store/apps/details?" + url.Values{
			"id": {app}, "hl": {storeLang}, "gl": {storeCountry}, "reviewId": {id},
		}.Encode(),
		Engagement: map[string]float64{},
	}

	if rv.at(2, &score) {
		it.Engagement["rating"] = score
	}

	if rv.at(6, &thumbs) {
		it.Engagement["thumbs_up"] = thumbs
	}

	if rv.at(5, &at) && len(at) > 0 && at[0] > 0 {
		t := time.Unix(int64(at[0]), 0).UTC()
		it.PublishedAt = &t
	}

	return it
}

// playReviews reads a page's reviews and the next page's token: the RPC's answer is a JSON string inside the
// envelope, its reviews first and the token second to last (google-play-scraper reviews._fetch_review_items).
func playReviews(body []byte) ([]playReview, string, error) {
	text := string(body)
	if i := strings.Index(text, "["); i >= 0 {
		text = text[i:]
	}

	var envelope [][]json.RawMessage
	if err := json.Unmarshal([]byte(text), &envelope); err != nil || len(envelope) == 0 || len(envelope[0]) < 3 {
		return nil, "", Fail(CodeBroken, "the answer is not Google Play's reviews")
	}

	var payload string
	if err := json.Unmarshal(envelope[0][2], &payload); err != nil {
		return nil, "", Fail(CodeBroken, "the answer is not Google Play's reviews: %s", err.Error())
	}

	var parts []json.RawMessage
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &parts); err != nil {
			return nil, "", Fail(CodeBroken, "the answer is not Google Play's reviews: %s", err.Error())
		}
	}

	var reviews []playReview
	if len(parts) > 0 {
		_ = json.Unmarshal(parts[0], &reviews)
	}

	token := ""

	if len(parts) >= 2 {
		var tail []json.RawMessage
		if json.Unmarshal(parts[len(parts)-2], &tail) == nil && len(tail) > 0 {
			_ = json.Unmarshal(tail[len(tail)-1], &token)
		}
	}

	return reviews, token, nil
}

// The App Store feed's spellings of its first page: Apple's CDN serves one empty while another has the reviews
// (obsei sources/appstore.py FIRST_PAGE); later pages have one spelling (NEXT_PAGE). Only the JSON form is read.
var (
	appStoreFirst = []string{
		"page=1/id=%s/sortby=mostrecent", "id=%s/sortby=mostrecent", "id=%s/sortBy=mostRecent", "id=%s",
	}
	appStoreNext = "page=%d/id=%s/sortby=mostrecent"
	appStoreID   = regexp.MustCompile(`^\d+$`)
)

type label struct {
	Label string `json:"label"`
}

type appStoreEntry struct {
	ID      label `json:"id"`
	Title   label `json:"title"`
	Content label `json:"content"`
	Rating  label `json:"im:rating"`
	Updated label `json:"updated"`
	Author  struct {
		Name label `json:"name"`
	} `json:"author"`
	Link struct {
		Attributes struct {
			Href string `json:"href"`
		} `json:"attributes"`
	} `json:"link"`
}

// appStoreBackend reads an app's newest App Store reviews in Vietnam: the app the query names by its id, else the
// first app of Apple's search for it in Vietnam's store (marketingskills teardown.mjs cmdReviews, itunes.apple.com
// search with entity software), then the public reviews feed page after page, newest first, until the last page, the
// stream's limit or a review older than the window (obsei appstore.py _reviews, marketingskills: pages 1 to 10, 300 ms
// apart). A review is kept only with its id, rating, date and text (obsei appstore._record).
func appStoreBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.ITunesURL)

	return Backend{
		Name: BackendAppStore, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			app, name := strings.TrimSpace(r.Query), ""
			if !appStoreID.MatchString(app) {
				v := url.Values{"term": {app}, "entity": {"software"}, "country": {storeCountry},
					"limit": {strconv.Itoa(appStoreSearch)}}

				var found struct {
					Results []struct {
						TrackID   int64  `json:"trackId"`
						TrackName string `json:"trackName"`
					} `json:"results"`
				}

				if err := callJSON(ctx, client, r, call{
					host: host, gap: appStoreGap, path: host + "/search", address: e.ITunesURL + "/search?" + v.Encode(),
				}, &found); err != nil {
					return Result{}, err
				}

				if len(found.Results) == 0 || found.Results[0].TrackID == 0 {
					return Result{}, Fail(CodeNotFound, "no app in the App Store of Vietnam for %q", app)
				}

				app, name = strconv.FormatInt(found.Results[0].TrackID, 10), found.Results[0].TrackName
			}

			if name == "" {
				name = app
			}

			from := dayStart(r.From)

			var items []Item

			for page := 1; page <= appStorePages && len(items) < r.Limit; page++ {
				spellings := []string{fmt.Sprintf(appStoreNext, page, app)}
				if page == 1 {
					spellings = spellings[:0]
					for _, s := range appStoreFirst {
						spellings = append(spellings, fmt.Sprintf(s, app))
					}
				}

				var entries []appStoreEntry

				for _, spelled := range spellings {
					var feed struct {
						Feed struct {
							Entry json.RawMessage `json:"entry"`
						} `json:"feed"`
					}

					address := fmt.Sprintf("%s/%s/rss/customerreviews/%s/json", e.ITunesURL, storeCountry, spelled)
					if err := callJSON(ctx, client, r, call{
						host: host, gap: appStoreGap, path: host + "/rss/customerreviews", address: address,
					}, &feed); err != nil {
						return Result{}, err
					}

					entries = appStoreEntries(feed.Feed.Entry)
					if len(entries) > 0 {
						break
					}
				}

				if len(entries) == 0 {
					break
				}

				older := false

				for _, en := range entries {
					published := parseDate(en.Updated.Label)
					rating, err := strconv.ParseFloat(en.Rating.Label, 64)

					if en.ID.Label == "" || err != nil || published == nil || en.Content.Label == "" {
						continue
					}

					if published.Before(from) {
						older = true
					}

					link := en.Link.Attributes.Href
					if link == "" {
						link = "https://apps.apple.com/" + storeCountry + "/app/id" + app
					}

					items = append(items, Item{
						NativeID: en.ID.Label, URL: link, Title: en.Title.Label, Text: en.Content.Label,
						Author: en.Author.Name.Label, Container: name, PublishedAt: published,
						Engagement: map[string]float64{"rating": rating},
					})
				}

				if older {
					break
				}
			}

			return Result{Items: items, Detail: "app " + app}, nil
		},
	}
}

// appStoreEntries are a feed's entries: a list, or one entry alone (obsei appstore._from_json).
func appStoreEntries(raw json.RawMessage) []appStoreEntry {
	var list []appStoreEntry
	if json.Unmarshal(raw, &list) == nil {
		return list
	}

	var one appStoreEntry
	if json.Unmarshal(raw, &one) == nil && one.ID.Label != "" {
		return []appStoreEntry{one}
	}

	return nil
}
