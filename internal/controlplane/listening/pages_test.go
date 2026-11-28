package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The sources of issue #1289 that read more than an API: YouTube through yt-dlp, a company's careers page, and the
// Vietnamese forums through the web search and a read of the thread.

// ytdlp is a yt-dlp that answers from fixtures and records how it was run.
type ytdlp struct {
	mu   sync.Mutex
	runs [][]string
	// search is what a search prints, or stderr and a failure.
	search, stderr string
}

func (y *ytdlp) run(_ context.Context, args ...string) ([]byte, string, error) {
	y.mu.Lock()
	y.runs = append(y.runs, args)
	y.mu.Unlock()

	switch {
	case slices.Contains(args, "--write-comments"):
		id := strings.TrimPrefix(args[len(args)-1], "https://www.youtube.com/watch?v=")
		out, _ := json.Marshal(map[string]any{"id": id, "channel_id": "UCvetc", "comments": []map[string]any{
			{"id": "c-" + id, "text": "VETC trừ tiền hai lần ở " + id, "author": "@minh", "like_count": 40,
				"timestamp": start.AddDate(0, 0, -1).Unix(), "parent": "root", "author_is_uploader": false},
			{"id": "u-" + id, "text": "Cảm ơn bạn đã xem", "author": "@VETC", "like_count": 3,
				"timestamp": start.AddDate(0, 0, -1).Unix(), "parent": "root", "author_is_uploader": true},
			{"id": "r-" + id, "text": "a reply", "author": "@x", "parent": "c-" + id},
		}})

		return out, "", nil
	case slices.Contains(args, "--write-subs"):
		template := args[slices.Index(args, "-o")+1]
		id := strings.TrimPrefix(args[len(args)-1], "https://www.youtube.com/watch?v=")
		vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHôm nay tôi qua trạm Long Thành và VETC trừ tiền hai lần " +
			"mất 40 nghìn đồng.\n"
		_ = os.WriteFile(strings.Replace(template, "%(id)s", id, 1)+".vi.vtt", []byte(vtt), 0o600)

		return nil, "", nil
	default:
		if y.stderr != "" {
			return nil, y.stderr, &exec.ExitError{}
		}

		return []byte(y.search), "", nil
	}
}

func (y *ytdlp) Runs(flag string) [][]string {
	y.mu.Lock()
	defer y.mu.Unlock()

	var out [][]string

	for _, r := range y.runs {
		if slices.Contains(r, flag) || flag == "" {
			out = append(out, r)
		}
	}

	return out
}

func video(id, title string, daysAgo int, views, likes, comments int) string {
	line, _ := json.Marshal(map[string]any{
		"id": id, "title": title, "channel": "Kênh " + id, "upload_date": start.AddDate(0, 0, -daysAgo).Format("20060102"),
		"view_count": views, "like_count": likes, "comment_count": comments, "description": "Mô tả " + id,
	})

	return string(line)
}

// Design row "YouTube": yt-dlp's search with every video's metadata (last30days youtube_yt.py, 8 videos), then the
// top comments of the 3 most watched (comment_sort=top, top-level only, the channel's own left out) and the captions
// of the 2 most watched (Vietnamese, then English, the original track first), each video an item whose text is what
// was said, each comment an item under its video.
func TestYouTubeReadsVideosCommentsAndTranscripts(t *testing.T) {
	t.Parallel()

	y := &ytdlp{search: strings.Join([]string{
		`{"id": "UCvetc", "ie_key": "YoutubeTab", "_type": "url"}`,
		video("v1", "VETC trừ tiền hai lần", 2, 120000, 3000, 400),
		video("v2", "Hướng dẫn nạp tiền VETC", 5, 50000, 800, 90),
		video("v3", "VETC review", 9, 900, 10, 3),
		video("v4", "VETC 2020", 900, 9e6, 1e5, 1e4),
	}, "\n")}

	a := one(t, keyless(newClock(), listening.Endpoints{YtDlp: y.run}), listening.SourceYouTube, "VETC")

	search := y.Runs("--flat-playlist")
	require.Len(t, search, 1, "the search is listed flat: a channel among its results is not opened whole")
	assert.Contains(t, search[0], "ytsearch8:VETC")
	assert.Contains(t, search[0], "--ignore-config")
	assert.Contains(t, search[0], "--no-cookies-from-browser")

	read := y.Runs("--no-download")
	require.Len(t, read, 1, "the videos listed are read with their metadata at once")
	assert.Contains(t, read[0], "https://www.youtube.com/watch?v=v1")
	assert.NotContains(t, read[0], "https://www.youtube.com/watch?v=UCvetc", "a channel is not read")

	comments := y.Runs("--write-comments")
	require.Len(t, comments, 3, "the three most watched videos in the window")
	assert.Contains(t, comments[0], "youtube:comment_sort=top;max_comments=7,7,0,0")

	subs := y.Runs("--write-subs")
	require.Len(t, subs, 2)
	assert.Equal(t, "vi-orig,en-orig,vi,en", subs[0][slices.Index(subs[0], "--sub-lang")+1])

	items := byNative(a.Items)
	assert.NotContains(t, items, "v4", "a video outside the window")
	assert.NotContains(t, items, "u-v1", "the channel's own comment")
	assert.NotContains(t, items, "r-v1", "a reply")

	v1 := items["v1"]
	assert.Equal(t, "https://www.youtube.com/watch?v=v1", v1.URL)
	assert.Equal(t, "Kênh v1", v1.Author)
	assert.Equal(t, map[string]float64{"views": 120000, "likes": 3000, "comments": 400}, v1.Engagement)
	assert.Contains(t, v1.Text, "VETC trừ tiền hai lần mất 40 nghìn đồng")

	c1 := items["c-v1"]
	assert.Equal(t, "v1", c1.Parent)
	assert.Equal(t, "https://www.youtube.com/watch?v=v1&lc=c-v1", c1.URL)
	assert.Equal(t, "@minh", c1.Author)
	assert.Equal(t, "VETC trừ tiền hai lần", c1.Container)
	require.NotNil(t, c1.EngagementScore)
}

// Design row "YouTube": with fewer than 3 videos in the window, the search is asked again for recent uploads only,
// with YouTube's upload-date filter, the smallest that holds the window (last30days _search_recent_uploads and
// _upload_date_filter: this month for a 30-day window).
func TestYouTubeAsksForRecentUploadsWhenTheWindowIsThin(t *testing.T) {
	t.Parallel()

	y := &ytdlp{search: strings.Join([]string{
		video("old1", "VETC 2020", 900, 9e6, 1e5, 1e4),
		video("new1", "VETC tháng này", 3, 500, 10, 2),
	}, "\n")}

	a := one(t, keyless(newClock(), listening.Endpoints{YtDlp: y.run}), listening.SourceYouTube, "VETC")

	listed := y.Runs("--flat-playlist")
	require.Len(t, listed, 2)
	assert.Contains(t, listed[1], "https://www.youtube.com/results?search_query=VETC&sp=EgIIBA%3D%3D")
	assert.Contains(t, byNative(a.Items), "new1")
}

// Design row "YouTube": a bot check is a refusal (Agent-Reach channels/youtube.py, PLATFORM_BLOCKED), a 429 a rate
// limit, and yt-dlp missing from the control plane is broken, never a clean zero.
func TestYouTubeFailuresHaveTheirCodes(t *testing.T) {
	t.Parallel()

	for stderr, code := range map[string]string{
		"ERROR: [youtube] abc: Sign in to confirm you're not a bot":    "refused",
		"ERROR: unable to download: HTTP Error 429: Too Many Requests": "rate_limited",
	} {
		y := &ytdlp{stderr: stderr}
		a := one(t, keyless(newClock(), listening.Endpoints{YtDlp: y.run}), listening.SourceYouTube, "VETC")
		assert.Equal(t, code, coverageOf(t, a, listening.SourceYouTube).Streams[0].Tried[0].Result, stderr)
	}

	missing := func(context.Context, ...string) ([]byte, string, error) { return nil, "", exec.ErrNotFound }
	a := one(t, keyless(newClock(), listening.Endpoints{YtDlp: missing}), listening.SourceYouTube, "VETC")
	cov := coverageOf(t, a, listening.SourceYouTube)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeBroken, cov.Code)
	assert.Contains(t, cov.Streams[0].Tried[0].Detail, "yt-dlp")
}

func jobPage(title, location, posted, about string) string {
	ld, _ := json.Marshal(map[string]any{
		"@context": "https://schema.org", "@type": "JobPosting", "title": title, "datePosted": posted,
		"description":        "<p>" + about + "</p>",
		"hiringOrganization": map[string]any{"@type": "Organization", "name": "VETC"},
		"jobLocation": map[string]any{"@type": "Place", "address": map[string]any{
			"@type": "PostalAddress", "addressLocality": location, "addressCountry": "VN"}},
	})

	return `<html><head><script type="application/ld+json">` + string(ld) + `</script></head><body></body></html>`
}

// Design row "Careers pages": the company's own careers page, found by a web search for its careers (last30days
// jobs.py _search_for_careers_url), its roles read from the schema.org JobPosting of the page or of the role pages it
// links on its own site (jobs.py extract_jsonld_jobs, jsonld_role_text); a role out of character for the company is
// marked: a founding role, a leader of a function, a specialized one, or a place no other role is in
// (hiring_signals.py _strategic_candidates).
func TestCareersReadsTheCompanysOwnPage(t *testing.T) {
	t.Parallel()

	jobs := map[string]string{
		"/jobs/head-ai": jobPage("Head of AI Research, Human Simulation", "Hà Nội", "2026-11-20",
			"Dẫn dắt nhóm nghiên cứu mô phỏng hành vi người lái xe qua trạm thu phí"),
		"/jobs/founding": jobPage("Founding Engineer", "Hà Nội", "2026-11-21",
			"Xây dựng nền tảng ví điện tử mới từ con số không cùng đội sáng lập"),
		"/jobs/backend": jobPage("Backend Engineer", "Hà Nội", "2026-11-22",
			"Phát triển dịch vụ Go xử lý giao dịch ETC hàng triệu lượt mỗi ngày"),
		"/jobs/sales": jobPage("Sales Executive", "Đà Nẵng", "2026-11-23",
			"Mở rộng mạng lưới đại lý dán thẻ tại miền Trung"),
	}

	site, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if page, ok := jobs[r.URL.Path]; ok {
			_, _ = io.WriteString(w, page)

			return
		}

		if r.URL.Path == "/careers" {
			_, _ = io.WriteString(w, `<a href="/jobs/head-ai">AI</a><a href="/jobs/founding">F</a>`+
				`<a href="/jobs/backend">B</a><a href="/jobs/sales">S</a><a href="https://other.example/jobs/x">X</a>`+
				`<a href="/about">About</a>`)

			return
		}

		http.NotFound(w, r)
	})

	var asked []string

	web := func(_ context.Context, query string, _ int) (websearch.Answer, error) {
		asked = append(asked, query)

		return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{
			{Title: "VETC news", URL: base + "/news"},
			{Title: "Tuyển dụng VETC", URL: base + "/careers"},
		}}, nil
	}

	s := keyless(newClock(), listening.Endpoints{})
	p := plan([]string{listening.SourceCareers}, "VETC")
	a, err := s.Search(t.Context(), p, web)
	require.NoError(t, err)

	assert.Equal(t, []string{"VETC careers jobs"}, asked)

	for _, r := range site.Requests() {
		assert.NotEqual(t, "/news", r.URL.Path, "a result that is not a careers page is not read")
		assert.NotEqual(t, "/about", r.URL.Path, "a link that is not a role is not read")
	}

	byTitle := map[string]listening.Item{}
	for _, it := range a.Items {
		byTitle[it.Title] = it
	}

	require.Len(t, byTitle, 4)
	assert.Equal(t, []string{"leadership", "specialized"}, byTitle["Head of AI Research, Human Simulation"].Signals)
	assert.Equal(t, []string{"founding"}, byTitle["Founding Engineer"].Signals)
	assert.Empty(t, byTitle["Backend Engineer"].Signals)
	assert.Equal(t, []string{"new-geo"}, byTitle["Sales Executive"].Signals)

	be := byTitle["Backend Engineer"]
	assert.Equal(t, base+"/jobs/backend", be.URL)
	assert.Equal(t, "VETC", be.Container)
	require.NotNil(t, be.PublishedAt)
	assert.Equal(t, "2026-11-22", be.PublishedAt.Format(time.DateOnly))
	assert.Equal(t, "Hà Nội, VN — Phát triển dịch vụ Go xử lý giao dịch ETC hàng triệu lượt mỗi ngày", be.Text)
}

// Design row "Forums": a web search `site:<forum> <query>`, then the thread read only where its robots.txt lets this
// control plane read it (RFC 9309, the group naming `hypomnema` before `*`) and its content signals allow an AI's
// input: voz.vn reserves its content to reference use, so it gets the search's excerpt and link only (research note
// §2), and so does a forum whose robots.txt refuses the thread.
func TestForumsReadOnlyTheThreadsTheyAllow(t *testing.T) {
	t.Parallel()

	thread := `<html><head><meta property="og:title" content="Nổ sập nhà ở phố Nguyễn Văn Tuyết">` +
		`<meta property="og:description" content="VETC trừ tiền 2 lần, ai bị giống không?"></head>` +
		`<body><article data-author="Kacee"><time datetime="2026-11-24T08:57:50+0700">24/11</time></article>` +
		`</body></html>`

	forum := func(robots string) (*site, string) {
		return startSite(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, robots)

				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, thread)
		})
	}

	voz, vozURL := forum("User-agent: *\nContent-Signal: search=yes,ai-train=no,use=reference\nAllow: /\n")
	tinhte, tinhteURL := forum("User-agent: *\nAllow: /\n\nUser-agent: hypomnema\nDisallow: /thread/\n")
	otofun, otofunURL := forum("User-agent: *\nDisallow: /account/\nAllow: /\n")

	threads := map[string]string{
		"voz.vn": vozURL + "/t/vetc-tru-tien.1283658/page-2", "tinhte.vn": tinhteURL + "/thread/vetc.4196450/",
		"otofun.net.vn": otofunURL + "/threads/vetc-tru-tien.1971431/",
	}

	web := func(_ context.Context, query string, _ int) (websearch.Answer, error) {
		site, q, _ := strings.Cut(strings.TrimPrefix(query, "site:"), " ")
		if q != "VETC" {
			return websearch.Answer{}, fmt.Errorf("asked %q", query)
		}

		return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{
			{Title: "VETC trừ tiền | " + site, URL: threads[site], Snippet: "VETC trừ tiền 2 lần ở " + site},
			{Title: "Forum home", URL: strings.Split(threads[site], "/t")[0] + "/forums/"},
		}}, nil
	}

	s := keyless(newClock(), listening.Endpoints{ForumURLs: map[string]string{
		listening.SourceVoz: vozURL, listening.SourceTinhte: tinhteURL, listening.SourceOtofun: otofunURL,
	}})
	a, err := s.Search(t.Context(), plan([]string{listening.SourceVoz, listening.SourceTinhte, listening.SourceOtofun},
		"VETC"), web)
	require.NoError(t, err)

	paths := func(s *site) []string {
		var out []string
		for _, r := range s.Requests() {
			out = append(out, r.URL.Path)
		}

		return out
	}

	assert.Equal(t, []string{"/robots.txt"}, paths(voz))
	assert.Equal(t, []string{"/robots.txt"}, paths(tinhte))
	assert.Equal(t, []string{"/robots.txt", "/threads/vetc-tru-tien.1971431/"}, paths(otofun))

	bySource := map[string]listening.Item{}
	for _, it := range a.Items {
		bySource[it.Source] = it
	}

	require.Len(t, bySource, 3)
	assert.Equal(t, "VETC trừ tiền 2 lần ở voz.vn", bySource["voz"].Text)
	assert.Equal(t, vozURL+"/t/vetc-tru-tien.1283658/", bySource["voz"].URL, "the thread, not its page")
	assert.Contains(t, coverageOf(t, a, listening.SourceVoz).Streams[0].Tried[0].Detail, "excerpt")

	o := bySource["otofun"]
	assert.Equal(t, "Nổ sập nhà ở phố Nguyễn Văn Tuyết", o.Title)
	assert.Equal(t, "VETC trừ tiền 2 lần, ai bị giống không?", o.Text)
	assert.Equal(t, "Kacee", o.Author)
	assert.Equal(t, "otofun.net.vn", o.Container)
	require.NotNil(t, o.PublishedAt)
	assert.Equal(t, time.Date(2026, 11, 24, 1, 57, 50, 0, time.UTC), *o.PublishedAt)

}
