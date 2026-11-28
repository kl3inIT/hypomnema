package listening

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Douyin read as the owner's account (issue #1362): only its search page in the account's tab, whose own search XHR
// carries the a_bogus signature the page computes, read as it arrives, as last30days-skill-cn reads it
// (douyin.py:1-12, the anonymous direct endpoint dropped for a_bogus; crawler_bridge.py:935-1001, the video tab,
// /aweme/v1/web/search/item and general/search/single, eight scrolls 1.2 s apart; MediaCrawler
// media_platform/douyin/client.py:121-134, read only, signs every call but the general search in the page). The
// window goes as the page's publish_time, the values of MediaCrawler's filter (field.py:38-43: 0 any time, 1 a day, 7 a
// week, 180 half a year; client.py:188 and 215-218, read only), which its search page's address carries
// (client.py:255, publish_time=0&sort_type=0 in the Referer). A captcha, 验证 in the page or captcha in its address,
// refuses the account (crawler_bridge.py:990-993).

const douyinURL = "https://www.douyin.com"

// douyinSearchXHR are the page's search requests (crawler_bridge.py:937).
var douyinSearchXHR = []string{"/aweme/v1/web/search/item", "/aweme/v1/web/general/search/single"}

// douyinAnswer is one search XHR's answer, the parts read.
type douyinAnswer struct {
	StatusCode *int   `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
	HasMore    *int   `json:"has_more"`
	Data       []struct {
		AwemeInfo *douyinAweme `json:"aweme_info"`
	} `json:"data"`
}

// douyinAweme is a video (douyin.py:209-240 parse_aweme).
type douyinAweme struct {
	AwemeID    jsonID    `json:"aweme_id"`
	Desc       string    `json:"desc"`
	CreateTime jsonCount `json:"create_time"`
	Author     struct {
		Nickname string `json:"nickname"`
	} `json:"author"`
	Statistics struct {
		Play     jsonCount `json:"play_count"`
		Digg     jsonCount `json:"digg_count"`
		Comments jsonCount `json:"comment_count"`
		Shares   jsonCount `json:"share_count"`
	} `json:"statistics"`
}

// douyinPublishTime is the window as Douyin's publish_time: a day, a week or half a year when one holds it, else any
// time (field.py:38-43, read only).
func douyinPublishTime(from, to time.Time) string {
	if from.IsZero() {
		return "0"
	}

	switch days := to.Sub(from).Hours() / 24; {
	case days <= 1:
		return "1"
	case days <= 7:
		return "7"
	case days <= 180:
		return "180"
	default:
		return "0"
	}
}

// douyinSearchURL is the video tab of Douyin's search, with the window as its publish_time.
func douyinSearchURL(query string, from, to time.Time) string {
	v := url.Values{"publish_time": {douyinPublishTime(from, to)}, "sort_type": {"0"}, "type": {"video"}}

	return douyinURL + "/search/" + url.PathEscape(query) + "?" + v.Encode()
}

func douyinPageBackend() Backend {
	return sessionBackend(Backend{
		Name: BackendDouyinPage, Host: "douyin.com",
		Search: signedPageSearch("douyin.com", chineseCadence, douyinPageRead),
	})
}

func douyinXHR(u *url.URL) bool {
	if !douyinSite.Owns(u.Hostname()) {
		return false
	}

	for _, p := range douyinSearchXHR {
		if strings.HasPrefix(u.Path, p) {
			return true
		}
	}

	return false
}

// douyinPageRead opens the search and reads the videos its page's search XHRs answer, scrolling for more until the
// limit or the eighth scroll (crawler_bridge.py:969-976). An answer of status 0, no video and has_more 0 is a clean
// zero, as xiaohongshu-mcp takes an empty search as read only when its answer says there is no more
// (search_ready.go:131-135); inferred for Douyin.
func douyinPageRead(ctx context.Context, r Request, page Page) (Result, error) {
	if err := page.Navigate(ctx, douyinSearchURL(r.Query, r.From, r.To)); err != nil {
		return Result{}, Fail(CodeBroken, "opening Douyin's search: %s", err.Error())
	}

	var (
		res    Result
		read   bool
		failed *SourceError
		seen   = map[string]bool{}
		more   = true
	)

	take := func() {
		for _, x := range pageXHRs(ctx, page, douyinXHR, seen) {
			if x.Status != http.StatusOK {
				failed = HTTPFailure(x.Status, nil, r.now())

				continue
			}

			items, end, err := douyinRead(x.Body)
			if err != nil {
				failed = AsFailure(err)

				continue
			}

			read, more = true, !end
			res.Items = appendNew(res.Items, items)
		}
	}

	// The first answer, waited for.
	for look := 1; look <= int(pageLoad/pagePoll) && !read; look++ {
		take()

		if f := siteThrottled(ctx, page, douyinSite, r.now()); f != nil {
			return Result{}, f
		}

		if read || failed != nil {
			break
		}

		if err := r.wait(ctx, pagePoll); err != nil {
			return Result{}, AsFailure(err)
		}
	}

	for i := 0; i < pageScrolls && read && more && len(res.Items) < r.Limit; i++ {
		if page.Evaluate(ctx, "window.scrollBy(0, 2400)", nil) != nil {
			break
		}

		if err := r.wait(ctx, pageScroll); err != nil {
			break
		}

		before := len(res.Items)
		take()

		if len(res.Items) == before && !more {
			break
		}
	}

	if !read && len(res.Items) == 0 {
		// A captcha first, as crawler_bridge.py:991-993 tells it before a sign-in wall.
		if l, err := landingOf(ctx, page); err == nil && strings.Contains(l.Text, "验证") {
			return Result{}, Fail(CodeRefused, "Douyin showed a verification (验证)")
		}
	}

	return pageResult(ctx, page, douyinSite, nil, r, res, read, failed)
}

// douyinRead reads one search XHR's answer: its videos, and whether it says there are no more.
func douyinRead(body []byte) ([]Item, bool, error) {
	var a douyinAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, false, Fail(CodeBroken, "Douyin's search answered what is not its JSON: %s", err.Error())
	}

	if a.StatusCode != nil && *a.StatusCode != 0 {
		return nil, false, Fail(CodeBroken, "Douyin's search answered status %d: %s", *a.StatusCode,
			oneLine(a.StatusMsg))
	}

	var items []Item

	for _, d := range a.Data {
		if d.AwemeInfo != nil {
			if it, ok := d.AwemeInfo.item(); ok {
				items = appendNew(items, []Item{it})
			}
		}
	}

	end := a.HasMore != nil && *a.HasMore == 0
	if len(items) == 0 && len(a.Data) > 0 {
		return nil, false, Fail(CodeBroken, "Douyin's search answered %d entries and no video", len(a.Data))
	}

	if len(items) == 0 && !end {
		return nil, false, Fail(CodeBroken, "Douyin's search answered no video and did not say it was the end")
	}

	return items, end, nil
}

// item is a video as an Item (douyin.py:209-240): its page, author, date and counts.
func (a *douyinAweme) item() (Item, bool) {
	if a.AwemeID == "" {
		return Item{}, false
	}

	if _, err := strconv.ParseUint(string(a.AwemeID), 10, 64); err != nil {
		return Item{}, false
	}

	return Item{
		NativeID: string(a.AwemeID), URL: douyinURL + "/video/" + string(a.AwemeID), Text: strings.TrimSpace(a.Desc),
		Author: a.Author.Nickname, PublishedAt: unixTime(float64(a.CreateTime)), Direct: true,
		Engagement: map[string]float64{
			"views": float64(a.Statistics.Play), "likes": float64(a.Statistics.Digg),
			"comments": float64(a.Statistics.Comments), "shares": float64(a.Statistics.Shares),
		},
	}, true
}
