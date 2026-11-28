package listening

import (
	"context"
	"crypto/md5" //nolint:gosec // Bilibili's WBI signature is MD5 by its protocol, not a security choice.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Chinese sources of issue #1289's addition (owner, 2026-11-28), keyless, as last30days-skill-cn reads them:
// Toutiao's search page with its embedded result cards and its hot board (toutiao.py), and Bilibili's search as a
// visitor, WBI-signed then legacy (bilibili.py; Agent-Reach channels/bilibili.py:1-9 dropped yt-dlp there for its
// 412s). Both answer from zm without a cookie (research note §8, probed 2026-11-28). Zhihu, Weibo, Douyin and
// Xiaohongshu need a signed-in account (#1362, chinasigned.go); Sogou's WeChat and Baidu answer empty or a captcha from zm.

const (
	toutiaoURL    = "https://so.toutiao.com"
	toutiaoHotURL = "https://www.toutiao.com"
	bilibiliURL   = "https://api.bilibili.com"

	// browserUA is a whole desktop Chrome's: these sites' WAFs answer 412 to a truncated one (last30days-skill-cn
	// http.DESKTOP_UA and AGENTS.md, "never hand-write a User-Agent"; zm's probe, research note §8).
	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/131.0.0.0 Safari/537.36"

	// toutiaoPages is the most pages of Toutiao's search read, toutiaoShort the fewest cards a page has before it
	// is the last, and toutiaoTries how often a page shell is asked again, each wait toutiaoWait longer
	// (last30days-skill-cn toutiao.py:49, 62, 125, 133: deep depth 3 pages, fewer than 5, 3 tries, 0.8 s × try).
	toutiaoPages = 3
	toutiaoShort = 5
	toutiaoTries = 3
	toutiaoWait  = 800 * time.Millisecond
	// toutiaoHotRelevance is how much of the topic a hot board entry must name (toutiao.py:245).
	toutiaoHotRelevance = 0.35

	// biliPageSize is Bilibili's page of results, and biliKeysTTL how long its WBI keys are kept
	// (last30days-skill-cn bilibili.py:45, 142).
	biliPageSize = 20
	biliKeysTTL  = 6 * time.Hour
)

// beijing is the time Bilibili's window is counted in (last30days-skill-cn dates.CST).
var beijing = time.FixedZone("CST", 8*3600)

// browserHeaders are a browser's headers for a Chinese site, its own page as Referer, and as Origin for a JSON call
// (last30days-skill-cn http.browser_headers).
func browserHeaders(referer string, asJSON bool) http.Header {
	h := http.Header{}
	h.Set("User-Agent", browserUA)
	h.Set("Sec-Ch-Ua", `"Chromium";v="131", "Google Chrome";v="131", "Not.A/Brand";v="99"`)
	h.Set("Sec-Ch-Ua-Mobile", "?0")
	h.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	h.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	h.Set("Referer", referer)

	if asJSON {
		h.Set("Accept", "application/json, text/plain, */*")

		if u, err := url.Parse(referer); err == nil {
			h.Set("Origin", u.Scheme+"://"+u.Host)
		}
	} else {
		h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}

	return h
}

// number is a count as these sites send it, a number or a string of one.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)

		return f, err == nil
	default:
		return 0, false
	}
}

var toutiaoCard = regexp.MustCompile(`<script[^>]*type="application/json"[^>]*>(\{"data":[\s\S]*?)</script>`)

// toutiaoBackend reads Toutiao's information search, so.toutiao.com, as a browser: each page embeds its results'
// JSON cards (title, source, publish time, read, comment and like counts) with no `_signature`; a page that is only
// its shell is asked again after a growing wait, the verify page is a refusal; then the hot board's entries that name
// the topic (last30days-skill-cn toutiao.py:1-12, 112-134, 142-208, 211-258). Its third route, a web search of
// toutiao.com, is the `web` source's.
func toutiaoBackend(e Endpoints, client *http.Client) Backend {
	host, hotHost := hostOf(e.ToutiaoURL), hostOf(e.ToutiaoHotURL)

	return Backend{
		// Off by default (opt-in): Toutiao's robots.txt allows only "/" and its user agreement limits automated
		// collection and commercial reuse without written leave; the owner decides (review round 1 of PR #1321).
		Name: BackendToutiao, Host: host, OptIn: true,
		Search: func(ctx context.Context, r Request) (Result, error) {
			var (
				items  []Item
				notes  []string
				failed *SourceError
			)

			pages := min(toutiaoPages, max(1, (r.Limit+techmemePage-1)/techmemePage))

			// The search's pages; a failure is kept, and the hot board is read whatever it was, as
			// last30days-skill-cn reads the board after a failed search (toutiao.py:54-80).
			for page := range pages {
				v := url.Values{"keyword": {r.Query}, "pd": {"information"}, "dvpf": {"pc"}, "source": {"input"}}
				if page > 0 {
					v.Set("page_num", strconv.Itoa(page))
				}

				cards, read, err := toutiaoPage(ctx, client, r, host, e.ToutiaoURL+"/search?"+v.Encode())
				if err == nil && !read && page == 0 {
					err = Fail(CodeBroken, "so.toutiao.com answered its page shell, without results, %d times", toutiaoTries)
				}

				if err != nil {
					f := AsFailure(err)
					failed = &SourceError{Code: f.Code, Detail: fmt.Sprintf("search page %d: %s", page+1, f.Detail),
						Until: f.Until}

					break
				}

				if !read {
					break
				}

				items = append(items, cards...)
				if len(cards) < toutiaoShort {
					break
				}
			}

			hot, err := toutiaoHot(ctx, client, r, hotHost, e.ToutiaoHotURL)
			if err != nil {
				notes = append(notes, "hot board not read: "+AsFailure(err).Error())

				if failed == nil {
					failed = AsFailure(err)
				}
			}

			items = append(items, hot...)
			if len(items) == 0 && failed != nil {
				return Result{}, &SourceError{
					Code: failed.Code, Detail: joinDetail(failed.Detail, strings.Join(notes, "; ")), Until: failed.Until,
				}
			}

			return Result{Items: items, Detail: strings.Join(notes, "; "), Failed: failed}, nil
		},
	}
}

// toutiaoPage reads one page of Toutiao's search: its cards, and whether it was read (cards, or Toutiao's own "no
// result" words); a shell is asked again up to toutiaoTries times.
func toutiaoPage(ctx context.Context, client *http.Client, r Request, host, address string) ([]Item, bool, error) {
	for try := range toutiaoTries {
		a, err := call{
			host: host, gap: time.Duration(try) * toutiaoWait, path: host + "/search", address: address,
			header: browserHeaders("https://so.toutiao.com/", false),
		}.do(ctx, client, r)
		if err != nil {
			return nil, false, err
		}

		body := string(a.body)
		head := body[:min(len(body), 4000)]

		// The verify page is where Toutiao sent the request, its address's path or host, never a keyword that holds
		// the word (last30days-skill-cn toutiao.py:127 looks at the whole address, query included).
		landed, _ := url.Parse(a.url)
		verify := landed != nil && (strings.Contains(landed.Path, "verify") || strings.Contains(landed.Host, "verify"))

		if verify || strings.Contains(head, "captcha") && !strings.Contains(body, "druid-card-data") {
			return nil, false, Fail(CodeRefused, "Toutiao answered its verify page")
		}

		var cards []Item

		for _, m := range toutiaoCard.FindAllStringSubmatch(body, -1) {
			var card struct {
				Data map[string]any `json:"data"`
			}

			if json.Unmarshal([]byte(m[1]), &card) == nil {
				if it, ok := toutiaoItem(card.Data); ok {
					cards = append(cards, it)
				}
			}
		}

		if len(cards) > 0 || strings.Contains(body, "没有找到") || strings.Contains(body, "无相关结果") ||
			strings.Contains(body, "抱歉，未找到") {
			return cards, true, nil
		}
	}

	return nil, false, nil
}

// toutiaoItem is a search card as an item (last30days-skill-cn toutiao._parse_card and _card_date): its publish time
// read first, as an epoch, its Beijing datetime else.
func toutiaoItem(d map[string]any) (Item, bool) {
	title := stripHTML(str(d["title"]))
	if title == "" {
		return Item{}, false
	}

	id := strings.TrimSpace(str(d["group_id"]))
	if id == "" {
		id = strings.TrimSpace(str(d["item_id"]))
	}

	link := ""
	if info, ok := d["info"].(map[string]any); ok {
		link = str(info["url"])
	}

	if _, err := strconv.ParseUint(id, 10, 64); link == "" && err == nil {
		link = "https://www.toutiao.com/group/" + id + "/"
	}

	if link == "" {
		link = cmpOr(str(d["article_url"]), str(d["display_url"]))
	}

	if strings.HasPrefix(link, "//") {
		link = "https:" + link
	}

	if link == "" {
		return Item{}, false
	}

	text := str(d["abstract"])
	if summary, ok := d["summary"].(map[string]any); ok && text == "" {
		text = str(summary["text"])
	}

	it := Item{
		NativeID: cmpOr(id, NormalizeURL(link)), URL: link, Title: title, Text: stripHTML(text),
		Author: cmpOr(str(d["source"]), str(d["media_name"])), Engagement: map[string]float64{},
	}

	for _, key := range []string{"publish_time", "display_time", "behot_time", "create_time"} {
		if sec, ok := number(d[key]); ok && sec > 0 {
			at := time.Unix(int64(sec), 0).UTC()
			it.PublishedAt = &at

			break
		}
	}

	if it.PublishedAt == nil {
		if at, err := time.ParseInLocation(time.DateTime, str(d["datetime"]), beijing); err == nil {
			at = at.UTC()
			it.PublishedAt = &at
		}
	}

	for name, key := range map[string]string{"reads": "read_count", "comments": "comment_count", "likes": "digg_count"} {
		if n, ok := number(d[key]); ok {
			it.Engagement[name] = n
		}
	}

	return it, true
}

func cmpOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}

	return b
}

// toutiaoHot is the hot board's entries that name the topic, undated (last30days-skill-cn toutiao.fetch_hot and
// _get_hot_related).
func toutiaoHot(ctx context.Context, client *http.Client, r Request, host, base string) ([]Item, error) {
	var board struct {
		Data []struct {
			Title    string `json:"Title"`
			URL      string `json:"Url"`
			HotValue any    `json:"HotValue"`
			Cluster  string `json:"ClusterIdStr"`
			Abstract string `json:"Abstract"`
		} `json:"data"`
	}

	if err := callJSON(ctx, client, r, call{
		host: host, path: host + "/hot-event/hot-board/", address: base + "/hot-event/hot-board/?origin=toutiao_pc",
		header: browserHeaders("https://www.toutiao.com/", true),
	}, &board); err != nil {
		return nil, err
	}

	topic := cmpOr(r.Topic, r.Query)

	var out []Item

	for _, entry := range board.Data {
		if entry.Title == "" || relevance(topic, entry.Title) < toutiaoHotRelevance {
			continue
		}

		link := entry.URL
		if strings.Contains(link, "toutiao.com/trending/") {
			link, _, _ = strings.Cut(link, "?")
		}

		it := Item{NativeID: cmpOr(entry.Cluster, NormalizeURL(link)), URL: link, Title: entry.Title, Text: entry.Abstract}
		if v, ok := number(entry.HotValue); ok {
			it.Engagement = map[string]float64{"hot_value": v}
		}

		out = append(out, it)
	}

	return out, nil
}

// mixinKeyOrder scrambles the WBI keys (last30days-skill-cn bilibili.MIXIN_KEY_ENC_TAB).
var mixinKeyOrder = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38,
	41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36,
	20, 34, 44, 52,
}

// signWBI is the parameters with Bilibili's WBI signature, `wts` and `w_rid` (last30days-skill-cn bilibili.sign_wbi:
// the keys sorted, the characters !'() and * dropped from values, the MD5 of the query and the mixin key).
func signWBI(params map[string]string, imgKey, subKey string, wts int64) url.Values {
	orig := imgKey + subKey

	var mixin strings.Builder

	for _, i := range mixinKeyOrder {
		if i < len(orig) {
			mixin.WriteByte(orig[i])
		}
	}

	key := mixin.String()[:min(32, mixin.Len())]
	out := url.Values{"wts": {strconv.FormatInt(wts, 10)}}

	for k, v := range params {
		out.Set(k, strings.Map(func(r rune) rune {
			if strings.ContainsRune("!'()*", r) {
				return -1
			}

			return r
		}, v))
	}

	sum := md5.Sum([]byte(out.Encode() + key)) //nolint:gosec // The protocol's MD5.
	out.Set("w_rid", hex.EncodeToString(sum[:]))

	return out
}

// biliVisitor is Bilibili's visitor state kept across searches: its cookies, from finger/spi, and its WBI keys,
// from nav, kept biliKeysTTL.
type biliVisitor struct {
	mu       sync.Mutex
	cookie   string
	img, sub string
	keysAt   time.Time
}

func (v *biliVisitor) forget() {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.cookie = ""
}

// visitor is the visitor's cookie header, asked of finger/spi when there is none (last30days-skill-cn
// bilibili._attach_buvid).
func (v *biliVisitor) visitor(ctx context.Context, client *http.Client, r Request, host, base string) (string, error) {
	v.mu.Lock()
	cookie := v.cookie
	v.mu.Unlock()

	if cookie != "" {
		return cookie, nil
	}

	var spi struct {
		Data struct {
			B3 string `json:"b_3"`
			B4 string `json:"b_4"`
		} `json:"data"`
	}

	if err := callJSON(ctx, client, r, call{
		host: host, path: host + "/x/frontend/finger/spi", address: base + "/x/frontend/finger/spi",
		header: browserHeaders("https://search.bilibili.com/", true),
	}, &spi); err != nil {
		return "", err
	}

	if spi.Data.B3 == "" {
		return "", Fail(CodeRefused, "Bilibili gave no visitor cookie")
	}

	cookie = "buvid3=" + spi.Data.B3 + "; buvid4=" + spi.Data.B4 + "; b_nut=" + strconv.FormatInt(r.now().Unix(), 10)

	v.mu.Lock()
	v.cookie = cookie
	v.mu.Unlock()

	return cookie, nil
}

// keys are the WBI keys, from the names of nav's two images (last30days-skill-cn bilibili._wbi_keys).
func (v *biliVisitor) keys(ctx context.Context, client *http.Client, r Request, host, base, cookie string) (string,
	string, error,
) {
	v.mu.Lock()
	img, sub, at := v.img, v.sub, v.keysAt
	v.mu.Unlock()

	if img != "" && r.now().Sub(at) < biliKeysTTL {
		return img, sub, nil
	}

	var nav struct {
		Data struct {
			WBI struct {
				Img string `json:"img_url"`
				Sub string `json:"sub_url"`
			} `json:"wbi_img"`
		} `json:"data"`
	}

	h := browserHeaders("https://search.bilibili.com/", true)
	h.Set("Cookie", cookie)

	if err := callJSON(ctx, client, r, call{
		host: host, path: host + "/x/web-interface/nav", address: base + "/x/web-interface/nav", header: h,
	}, &nav); err != nil {
		return "", "", err
	}

	name := func(u string) string {
		base := u[strings.LastIndex(u, "/")+1:]
		name, _, _ := strings.Cut(base, ".")

		return name
	}

	img, sub = name(nav.Data.WBI.Img), name(nav.Data.WBI.Sub)
	if img == "" || sub == "" {
		return "", "", Fail(CodeRefused, "Bilibili's nav gave no WBI keys")
	}

	v.mu.Lock()
	v.img, v.sub, v.keysAt = img, sub, r.now()
	v.mu.Unlock()

	return img, sub, nil
}

// biliRisk reads Bilibili's risk answers as a refusal: HTTP 412 (last30days-skill-cn bilibili.py:155-156).
func biliRisk(status int, header http.Header, now time.Time) *SourceError {
	if status == http.StatusPreconditionFailed {
		return Fail(CodeRefused, "HTTP 412: Bilibili's risk control")
	}

	return HTTPFailure(status, header, now)
}

// bilibiliBackend searches Bilibili's videos as a visitor: its cookies from finger/spi, a whole browser's headers and
// the search page as Referer, the WBI-signed search with the window as pubtime_begin_s and pubtime_end_s, ordered by
// rank then by clicks (the default depth's plan), and on a risk answer (HTTP 412, code -412 or -352) fresh cookies
// and the legacy search (last30days-skill-cn bilibili.py:1-17, 73-162, 186-217). An answer of code 0 that holds only a
// `v_voucher` is a risk check too, so a refusal: last30days-skill-cn reads it as no results (bilibili.py:119-127),
// which the coverage rule of #1287 forbids (no reference handles it; research note §8).
func bilibiliBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.BilibiliURL)
	visitor := &biliVisitor{}

	return Backend{
		Name: BackendBilibili, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			begin := time.Date(r.From.In(beijing).Year(), r.From.In(beijing).Month(), r.From.In(beijing).Day(), 0, 0,
				0, 0, beijing)
			end := time.Date(r.To.In(beijing).Year(), r.To.In(beijing).Month(), r.To.In(beijing).Day(), 23, 59, 59, 0,
				beijing)

			var (
				items  []Item
				notes  []string
				legacy bool
				// risk is the risk answer that sent the search to the legacy one: kept, so a legacy search that
				// finds nothing is still that refusal (last30days-skill-cn bilibili.py:204-213, 236-237: no items
				// and errors raise).
				risk *SourceError
			)

			for _, order := range []string{"totalrank", "click"} {
				params := map[string]string{
					"search_type": "video", "keyword": r.Query, "page": "1", "page_size": strconv.Itoa(biliPageSize),
					"order": order, "pubtime_begin_s": strconv.FormatInt(begin.Unix(), 10),
					"pubtime_end_s": strconv.FormatInt(end.Unix(), 10),
				}

				found, err := biliSearch(ctx, client, r, e.BilibiliURL, host, visitor, params, legacy)
				if f := failureOf(err); f != nil && f.Code == CodeRefused && !legacy {
					// Fresh visitor cookies and the legacy search, as last30days-skill-cn does on a risk answer.
					visitor.forget()

					risk, legacy = f, true
					notes = append(notes, "the WBI search met Bilibili's risk control ("+f.Detail+"); the legacy search "+
						"was asked")
					found, err = biliSearch(ctx, client, r, e.BilibiliURL, host, visitor, params, legacy)
				}

				if err != nil {
					f := AsFailure(err)
					if len(items) == 0 {
						return Result{}, &SourceError{
							Code: f.Code, Detail: strings.Join(append(notes, f.Detail), "; "), Until: f.Until,
						}
					}

					// Read in part: the order that failed keeps its code beside the items read.
					return Result{Items: items, Detail: strings.Join(notes, "; "), Failed: &SourceError{
						Code: f.Code, Detail: "order " + order + ": " + f.Detail, Until: f.Until,
					}}, nil
				}

				for _, it := range found {
					if !slices.ContainsFunc(items, func(o Item) bool { return o.NativeID == it.NativeID }) {
						items = append(items, it)
					}
				}
			}

			// Nothing usable after the window is nothing read: the risk answer stays the stream's (the window is the
			// core's, inWindow; last30days-skill-cn bilibili.py:204-213, 236-237 raises when no item is left).
			if risk != nil && !slices.ContainsFunc(items, func(it Item) bool { return inDays(it.PublishedAt, r.From, r.To) }) {
				return Result{}, &SourceError{
					Code: CodeRefused, Detail: strings.Join(append(notes, "the legacy search found nothing in the window"),
						"; "),
					Until: risk.Until,
				}
			}

			return Result{Items: items, Detail: strings.Join(notes, "; ")}, nil
		},
	}
}

// failureOf is AsFailure, nil for no error.
func failureOf(err error) *SourceError {
	if err == nil {
		return nil
	}

	return AsFailure(err)
}

// biliSearch asks one search, WBI-signed or legacy, and reads its answer's code: -412 and -352 are risk control, and
// so is a code 0 that holds only a v_voucher.
func biliSearch(ctx context.Context, client *http.Client, r Request, base, host string, visitor *biliVisitor,
	params map[string]string, legacy bool,
) ([]Item, error) {
	cookie, err := visitor.visitor(ctx, client, r, host, base)
	if err != nil {
		return nil, err
	}

	h := browserHeaders("https://search.bilibili.com/", true)
	h.Set("Cookie", cookie)

	path := "/x/web-interface/search/type"
	query := url.Values{}

	for k, v := range params {
		query.Set(k, v)
	}

	if !legacy {
		img, sub, err := visitor.keys(ctx, client, r, host, base, cookie)
		if err != nil {
			return nil, err
		}

		path = "/x/web-interface/wbi/search/type"
		query = signWBI(params, img, sub, r.now().Unix())
	}

	var answer struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}

	if err := callJSON(ctx, client, r, call{
		host: host, path: host + path, address: base + path + "?" + query.Encode(), header: h, failure: biliRisk,
	}, &answer); err != nil {
		return nil, err
	}

	code := 0
	if answer.Code != nil {
		code = *answer.Code
	}

	switch {
	case code == -412 || code == -352:
		return nil, Fail(CodeRefused, "code %d: Bilibili's risk control", code)
	case code != 0:
		return nil, Fail(CodeBroken, "code %d: %s", code, oneLine(answer.Message))
	}

	var data struct {
		Result  []map[string]any `json:"result"`
		Voucher string           `json:"v_voucher"`
	}

	_ = json.Unmarshal(answer.Data, &data)

	if len(data.Result) == 0 && data.Voucher != "" {
		return nil, Fail(CodeRefused, "Bilibili answered code 0 with only a v_voucher, its risk check, and no results")
	}

	items := make([]Item, 0, len(data.Result))

	for _, v := range data.Result {
		if it, ok := biliItem(v); ok {
			items = append(items, it)
		}
	}

	return items, nil
}

// biliItem is a video of Bilibili's search as an item (last30days-skill-cn bilibili._parse_video).
func biliItem(v map[string]any) (Item, bool) {
	bvid := str(v["bvid"])
	link := str(v["arcurl"])

	if bvid != "" {
		link = "https://www.bilibili.com/video/" + bvid
	}

	if link == "" {
		return Item{}, false
	}

	text := stripHTML(str(v["description"]))
	if text == "-" {
		text = ""
	}

	it := Item{
		NativeID: cmpOr(bvid, NormalizeURL(link)), URL: link, Title: stripHTML(str(v["title"])), Text: text,
		Author: str(v["author"]), Engagement: map[string]float64{},
	}

	if sec, ok := number(v["pubdate"]); ok && sec > 0 {
		at := time.Unix(int64(sec), 0).UTC()
		it.PublishedAt = &at
	}

	for name, keys := range map[string][]string{
		"views": {"play"}, "danmaku": {"video_review", "danmaku"}, "comments": {"review", "comment"},
		"favorites": {"favorites"}, "likes": {"like"},
	} {
		for _, k := range keys {
			if n, ok := number(v[k]); ok && !math.IsNaN(n) {
				it.Engagement[name] = n

				break
			}
		}
	}

	return it, true
}
