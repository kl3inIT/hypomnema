package listening

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/signin"
)

// Zhihu read as the owner's account (issue #1362), as last30days-skill-cn reads it (zhihu.py:1-12): its search_v3 API
// with the account's cookie, a search of 20 at most (zhihu.py:112), Zhihu's search page as Referer; else, when the API
// answers something that is not its search, the same search asked by Zhihu's own page in the account's tab, whose
// search_v3 XHR is read as it arrives (crawler_bridge.py:1078-1101, search?type=content); 401 or 403 is the session's
// end or a verification asked (zhihu.py:119): need_login. The hot list and the site search of zhihu.py:74-83 are not
// read: the web search on its site is the source's last backend (signed.go siteSearchBackend).

const (
	zhihuURL   = "https://www.zhihu.com"
	zhihuLimit = 20
)

// zhihuAnswer is search_v3's answer, the parts read (zhihu.py:126-137 parse_search_payload).
type zhihuAnswer struct {
	Data []struct {
		Type   string      `json:"type"`
		Object zhihuObject `json:"object"`
	} `json:"data"`
	Paging struct {
		IsEnd *bool `json:"is_end"`
	} `json:"paging"`
	Error *struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		NeedLogin bool   `json:"need_login"`
	} `json:"error"`
}

// zhihuObject is one result (zhihu.py:244-292 _parse_search_result).
type zhihuObject struct {
	ID       jsonID `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Name     string `json:"name"`
	Excerpt  string `json:"excerpt"`
	Content  string `json:"content"`
	URL      string `json:"url"`
	Question *struct {
		ID    jsonID `json:"id"`
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"question"`
	Author *struct {
		Name string `json:"name"`
	} `json:"author"`
	CreatedTime  jsonCount `json:"created_time"`
	UpdatedTime  jsonCount `json:"updated_time"`
	Created      jsonCount `json:"created"`
	VoteupCount  jsonCount `json:"voteup_count"`
	CommentCount jsonCount `json:"comment_count"`
	Collected    jsonCount `json:"collected_count"`
	Favlists     jsonCount `json:"favlists_count"`
}

// zhihuAPIBackend is search_v3 with the account's cookie.
func zhihuAPIBackend(client *http.Client) Backend {
	return sessionBackend(Backend{
		Name: BackendZhihuAPI, Host: "zhihu.com",
		Search: signedSearch("zhihu.com", chineseCadence, func(ctx context.Context, r Request,
			jar []browser.Cookie,
		) (Result, error) {
			v := url.Values{"t": {"general"}, "q": {r.Query}, "offset": {"0"},
				"limit": {strconv.Itoa(min(r.Limit, zhihuLimit))}}

			h := browserHeaders(zhihuURL+"/search", true)
			h.Set("Cookie", zhihuSite.Header(jar, "www.zhihu.com"))

			a, err := call{
				address: zhihuURL + "/api/v4/search_v3?" + v.Encode(), header: h, json: true, failure: zhihuFailure,
				userAgent: browserUA,
			}.do(ctx, client, r)
			if err != nil {
				return Result{}, err
			}

			return zhihuRead(a.body, "search_v3")
		}),
	})
}

// zhihuFailure is what a Zhihu status means: 401 and 403 the session's end or a verification asked (zhihu.py:119),
// the rest as HTTPFailure.
func zhihuFailure(status int, h http.Header, now time.Time) *SourceError {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Fail(CodeNeedLogin, "Zhihu answered HTTP %d: the account's session ended or Zhihu asks it to verify",
			status)
	}

	return HTTPFailure(status, h, now)
}

// zhihuRead reads one search_v3 answer: its results, or a clean zero when Zhihu says it is the end; an empty answer
// that does not say so is no zero (radar facebook-topic-adapter.md:9; xiaohongshu-mcp search_ready.go:131-135 takes an
// empty search as read only when its answer says there is no more).
func zhihuRead(body []byte, via string) (Result, error) {
	var a zhihuAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return Result{}, Fail(CodeBroken, "Zhihu's %s answered what is not its JSON: %s", via, err.Error())
	}

	if a.Error != nil {
		if a.Error.NeedLogin {
			return Result{}, Fail(CodeNeedLogin, "Zhihu asked the account to sign in again (%s)", oneLine(a.Error.Message))
		}

		return Result{}, Fail(CodeBroken, "Zhihu's %s failed: code %d, %s", via, a.Error.Code, oneLine(a.Error.Message))
	}

	var items []Item

	seen := map[string]bool{}

	for _, e := range a.Data {
		if it, ok := e.Object.item(e.Type); ok && !seen[it.NativeID] {
			seen[it.NativeID] = true
			items = append(items, it)
		}
	}

	switch {
	case len(items) > 0:
		return Result{Items: items, Detail: fmt.Sprintf("Zhihu's %s read %d results as the account", via, len(items))},
			nil
	case len(a.Data) == 0 && a.Paging.IsEnd != nil && *a.Paging.IsEnd:
		return Result{Detail: "Zhihu's " + via + " has no result"}, nil
	default:
		return Result{}, Fail(CodeBroken, "Zhihu's %s answered %d entries and no result it names, nor its end", via,
			len(a.Data))
	}
}

// item is a result as an Item: an answer, an article or a question by its address (zhihu.py:260-272), dated by its
// creation (zhihu.py:274-277).
func (o zhihuObject) item(entryType string) (Item, bool) {
	title := stripHTML(cmpOr(o.Title, o.Name))
	if title == "" && o.Question != nil {
		title = stripHTML(cmpOr(o.Question.Name, o.Question.Title))
	}

	if title == "" {
		return Item{}, false
	}

	kind := cmpOr(o.Type, entryType)

	var id, link string

	switch kind {
	case "answer":
		if o.Question == nil || o.Question.ID == "" || o.ID == "" {
			return Item{}, false
		}

		id, link = "answer:"+string(o.ID), zhihuURL+"/question/"+string(o.Question.ID)+"/answer/"+string(o.ID)
	case "article", "question":
		if o.ID == "" {
			return Item{}, false
		}

		id, link = kind+":"+string(o.ID), zhihuURL+"/question/"+string(o.ID)
		if kind == "article" {
			link = "https://zhuanlan.zhihu.com/p/" + string(o.ID)
		}
	default:
		link = strings.Replace(o.URL, "api.zhihu.com/questions/", "www.zhihu.com/question/", 1)
		id = NormalizeURL(link)
	}

	if link == "" {
		return Item{}, false
	}

	author := ""
	if o.Author != nil {
		author = o.Author.Name
	}

	it := Item{
		NativeID: id, URL: link, Title: title, Text: cut(stripHTML(cmpOr(o.Excerpt, o.Content)), 300), Author: author,
		Direct: true, Engagement: map[string]float64{
			"voteups": float64(o.VoteupCount), "comments": float64(o.CommentCount),
			"collects": float64(max(o.Collected, o.Favlists)),
		},
	}

	for _, t := range []jsonCount{o.CreatedTime, o.UpdatedTime, o.Created} {
		if at := unixTime(float64(t)); at != nil {
			it.PublishedAt = at

			break
		}
	}

	return it, true
}

// zhihuPageBackend is Zhihu's search page in the account's tab, its own search_v3 XHR read.
func zhihuPageBackend() Backend {
	return sessionBackend(Backend{
		Name: BackendZhihuPage, Host: "zhihu.com",
		Search: signedPageSearch("zhihu.com", chineseCadence, zhihuPageRead),
	})
}

// zhihuXHR says an address is the page's search_v3 (crawler_bridge.py:1086).
func zhihuXHR(u *url.URL) bool {
	return zhihuSite.Owns(u.Hostname()) && strings.HasPrefix(u.Path, "/api/v4/search_v3")
}

// zhihuPageRead opens the search in the account's tab and reads the search_v3 answers its page receives, the first,
// then one scroll's (crawler_bridge.py:1090-1101).
func zhihuPageRead(ctx context.Context, r Request, page Page) (Result, error) {
	address := zhihuURL + "/search?" + url.Values{"type": {"content"}, "q": {r.Query}}.Encode()
	if err := page.Navigate(ctx, address); err != nil {
		return Result{}, Fail(CodeBroken, "opening Zhihu's search: %s", err.Error())
	}

	var (
		res    Result
		read   bool
		failed *SourceError
		seen   = map[string]bool{}
	)

	take := func() {
		for _, x := range pageXHRs(ctx, page, zhihuXHR, seen) {
			if x.Status != http.StatusOK {
				failed = zhihuFailure(x.Status, nil, r.now())

				continue
			}

			got, err := zhihuRead(x.Body, "search page")
			if err != nil {
				failed = AsFailure(err)

				continue
			}

			read = true

			for _, it := range got.Items {
				if !containsItem(res.Items, it) {
					res.Items = append(res.Items, it)
				}
			}
		}
	}

	for look := 1; look <= int(pageLoad/pagePoll); look++ {
		take()

		if f := siteThrottled(ctx, page, zhihuSite, r.now()); f != nil {
			return Result{}, f
		}

		if read || failed != nil && failed.Code == CodeNeedLogin {
			break
		}

		if err := r.wait(ctx, pagePoll); err != nil {
			return Result{}, AsFailure(err)
		}
	}

	if read && len(res.Items) < r.Limit {
		// One scroll more, as crawler_bridge.py:1097-1100 does once the first answer came.
		if page.Evaluate(ctx, "window.scrollBy(0, 2400)", nil) == nil && r.wait(ctx, 1500*time.Millisecond) == nil {
			take()
		}
	}

	return pageResult(ctx, page, zhihuSite, nil, r, res, read, failed)
}

// pageResult settles a page's read: its items, a rate limit or a captcha the page met, else a clean zero when the
// search's answer said so, else the barrier its landing shows, the search's own failure, or broken.
func pageResult(ctx context.Context, page Page, s signin.Site, refused []int, r Request, res Result, read bool,
	failed *SourceError,
) (Result, error) {
	if f := siteThrottled(ctx, page, s, r.now(), refused...); f != nil {
		if len(res.Items) == 0 {
			return Result{}, f
		}

		f.restBackend = true
		res.Failed = f

		return res, nil
	}

	if len(res.Items) > r.Limit {
		res.Items = res.Items[:r.Limit]
	}

	switch {
	case len(res.Items) > 0:
		res.Detail = fmt.Sprintf("%s's search page showed %d results", s.Name, len(res.Items))

		return res, nil
	case read && failed == nil:
		return Result{Detail: s.Name + "'s search page has no result"}, nil
	}

	l, err := landingOf(ctx, page)
	if err == nil {
		if f := pageBarrier(s, l); f != nil {
			return Result{}, f
		}
	}

	if failed != nil {
		return Result{}, failed
	}

	return Result{}, Fail(CodeBroken, "%s's search page sent no search the page could be read by in time", s.Name)
}

func containsItem(items []Item, it Item) bool {
	for _, o := range items {
		if o.NativeID == it.NativeID {
			return true
		}
	}

	return false
}
