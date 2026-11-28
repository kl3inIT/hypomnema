package listening

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// YouTube through yt-dlp, keyless (issue #1289): its search with each video's metadata, then the top comments of the
// most watched videos and the captions of the first few, as last30days reads it (youtube_yt.py) and Agent-Reach's
// YouTube channel (channels/youtube.py). The Data API, keyed, is issue #1293's.

// Command runs a program with its arguments and gives what it printed, its standard error and how it ended.
type Command func(ctx context.Context, args ...string) (stdout []byte, stderr string, err error)

const (
	// youtubeVideos is how many videos a search reads: last30days' default depth (youtube_yt.DEPTH_CONFIG).
	youtubeVideos = 8
	// youtubeCommented and youtubeComments: the comments of the 3 most watched videos, 5 each, read top first
	// (last30days enrich_with_comments, max_videos 3 and max_comments 5; _ytdlp_comments_result asks 2 more for the
	// empty ones).
	youtubeCommented = 3
	youtubeComments  = 5
	// youtubeTranscripts is how many of the most watched videos' captions are read (last30days TRANSCRIPT_LIMITS,
	// default 2).
	youtubeTranscripts = 2
	// youtubeSearchTimeout bounds a search, youtubeCallTimeout a comments or captions read (last30days
	// _SEARCH_TIMEOUT 120 s, _COMMENT_TIMEOUT 20 s, _TRANSCRIPT_TIMEOUT 30 s).
	youtubeSearchTimeout = 120 * time.Second
	youtubeCommentTimeout = 20 * time.Second
	youtubeCaptionTimeout = 30 * time.Second
	// youtubeHost is the host yt-dlp's calls go to, one at a time (pace.go).
	youtubeHost = "youtube.com"
	// youtubeRecent is how many videos in the window are enough before recent uploads are asked (last30days
	// search_youtube: "_count_in_window(...) < 3").
	youtubeRecent = 3
	// youtubeHighlights is how many quotable sentences of a transcript an item keeps (last30days
	// extract_transcript_highlights, limit 5).
	youtubeHighlights = 5
)

// youtubeLangs are the caption languages asked, the original speech's track of each first (last30days
// _ytdlp_request_langs: every "<lang>-orig" before the plain codes; Vietnamese, then English, for this owner's
// topics, where last30days defaults to en,es,pt and Agent-Reach to zh-Hans,zh,en).
const youtubeLangs = "vi-orig,en-orig,vi,en"

// runYtDlp is the yt-dlp on the control plane's PATH.
func runYtDlp(ctx context.Context, args ...string) ([]byte, string, error) {
	path, err := exec.LookPath("yt-dlp")
	if err != nil {
		return nil, "", err
	}

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()

	return stdout.Bytes(), stderr.String(), err
}

// ytFailure is a failed yt-dlp run as a code: a bot check is a refusal (Agent-Reach youtube._run_ytdlp,
// PLATFORM_BLOCKED), a 429 a rate limit, a sign-in wall for an age-gated video a sign-in, a missing video not found,
// a timeout a timeout, yt-dlp missing broken (last30days classify_run_failure).
func ytFailure(ctx context.Context, stderr string, err error) *SourceError {
	text := strings.ToLower(stderr)
	line := oneLine(lastLine(stderr))

	switch {
	case errors.Is(err, exec.ErrNotFound):
		return Fail(CodeBroken, "yt-dlp is not installed on the control plane")
	case ctx.Err() != nil:
		return Fail(CodeTimeout, "yt-dlp did not finish in time")
	case strings.Contains(text, "not a bot"):
		return Fail(CodeRefused, "YouTube asked to confirm this is not a bot: %s", line)
	case strings.Contains(text, "429") || strings.Contains(text, "too many requests"):
		return Fail(CodeRateLimited, "%s", line)
	case strings.Contains(text, "sign in") || strings.Contains(text, "login required"):
		return Fail(CodeNeedLogin, "%s", line)
	case strings.Contains(text, "video unavailable") || strings.Contains(text, "private video"):
		return Fail(CodeNotFound, "%s", line)
	default:
		if line == "" {
			line = "it ended without saying why"
		}

		return Fail(CodeBroken, "yt-dlp failed: %s", line)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")

	return lines[len(lines)-1]
}

// ytRun runs yt-dlp in its turn at YouTube, bounded by timeout.
func ytRun(ctx context.Context, run Command, r Request, timeout time.Duration, args ...string) ([]byte, error) {
	if r.Pace != nil {
		done, err := r.Pace(ctx, youtubeHost, 0)
		if err != nil {
			return nil, AsFailure(err)
		}

		defer done()
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, err := run(ctx, args...)
	if err != nil && len(bytes.TrimSpace(stdout)) == 0 {
		return nil, ytFailure(ctx, stderr, err)
	}

	return stdout, nil
}

// ytBase are the options every run takes: no config file and no browser's cookies, keyless (last30days).
var ytBase = []string{"--ignore-config", "--no-cookies-from-browser", "--no-warnings"}

type ytVideo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Channel     string   `json:"channel"`
	Uploader    string   `json:"uploader"`
	UploadDate  string   `json:"upload_date"`
	Views       *float64 `json:"view_count"`
	Likes       *float64 `json:"like_count"`
	Comments    *float64 `json:"comment_count"`
	Description string   `json:"description"`
}

var ytVideoID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// youtubeBackend searches YouTube with yt-dlp: `ytsearch8:<query>` listed flat (Agent-Reach), then every video found
// read with its metadata (last30days search_youtube: the flat list gives no dates), keeps the videos in the window, then reads the top
// comments of the most watched (last30days _ytdlp_comments_result) and the captions of the first few (last30days
// _fetch_transcript_ytdlp, Agent-Reach _ytdlp_subtitle). A video's text is what was said, its transcript's most
// quotable sentences, else its description; each comment is an item under its video. Comments and captions are
// enrichment: one that fails is named in the detail, and a refusal or a rate limit stops the rest of this search's
// (last30days _youtube_block_scope).
func youtubeBackend(run Command) Backend {
	return Backend{
		Name: BackendYtDlp, Host: youtubeHost,
		Search: func(ctx context.Context, r Request) (Result, error) {
			q := strings.Join(strings.Fields(r.Query), " ")

			// The search's results as they are listed, without opening them: a channel among them would otherwise be
			// opened whole, every video of it (Agent-Reach _ytdlp_search's --flat-playlist; last30days' search without
			// it read 150 videos in 3 minutes for "VETC" on 2026-11-28, the VETC channel being a result).
			listed, err := ytRun(ctx, run, r, youtubeSearchTimeout, append(slices.Clone(ytBase), "--flat-playlist",
				"--dump-json", fmt.Sprintf("ytsearch%d:%s", youtubeVideos, q))...)
			if err != nil {
				return Result{}, err
			}

			asked := map[string]bool{}

			videos, err := youtubeRead(ctx, run, r, listed, asked)
			if err != nil {
				return Result{}, err
			}

			inWindow := youtubeInWindow(videos, r)

			// Few videos in the window: relevance has filled the results with old ones, so the results page is
			// asked again for recent uploads only, and what it adds is read (last30days search_youtube's retry,
			// _search_recent_uploads, "fewer than 3 in window").
			if sp := uploadFilter(r); len(inWindow) < youtubeRecent && sp != "" {
				page := "https://www.youtube.com/results?" + url.Values{"search_query": {q}, "sp": {sp}}.Encode()

				recent, err := ytRun(ctx, run, r, youtubeSearchTimeout, append(slices.Clone(ytBase), "--flat-playlist",
					"--dump-json", "--playlist-end", strconv.Itoa(youtubeVideos), page)...)
				if err == nil {
					more, err := youtubeRead(ctx, run, r, recent, asked)
					if err == nil {
						inWindow = append(inWindow, youtubeInWindow(more, r)...)
					}
				}
			}

			if len(asked) == 0 {
				return Result{Detail: "the search listed no video"}, nil
			}

			// The most watched first, as last30days sorts and enriches them (search_youtube, _total_engagement).
			slices.SortStableFunc(inWindow, func(a, b Item) int { return cmp.Compare(watched(b), watched(a)) })

			var (
				items   []Item
				notes   []string
				blocked *SourceError
			)

			for i, v := range inWindow {
				if i < youtubeTranscripts && blocked == nil {
					said, f := youtubeSaid(ctx, run, r, v.NativeID, r.Topic)
					switch {
					case f != nil:
						notes = append(notes, "captions of "+v.NativeID+" not read: "+f.Error())
						if stopsHost(f.Code) {
							blocked = f
						}
					case said != "":
						v.Text = said
					}
				}

				items = append(items, v)

				if i < youtubeCommented && blocked == nil {
					comments, f := youtubeCommentsOf(ctx, run, r, v)
					if f != nil {
						notes = append(notes, "comments of "+v.NativeID+" not read: "+f.Error())
						if stopsHost(f.Code) {
							blocked = f
						}
					}

					items = append(items, comments...)
				}
			}

			return Result{Items: items, Detail: strings.Join(notes, "; ")}, nil
		},
	}
}

// youtubeRead reads the videos a flat listing names, each once (asked), with their metadata.
func youtubeRead(ctx context.Context, run Command, r Request, listed []byte, asked map[string]bool) ([]Item, error) {
	var urls []string

	for line := range strings.SplitSeq(string(listed), "\n") {
		var e struct {
			ID    string `json:"id"`
			IEKey string `json:"ie_key"`
			Type  string `json:"_type"`
		}

		if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) != nil || !ytVideoID.MatchString(e.ID) ||
			e.IEKey != "" && e.IEKey != "Youtube" || e.Type == "playlist" || asked[e.ID] {
			continue
		}

		asked[e.ID] = true
		urls = append(urls, "https://www.youtube.com/watch?v="+e.ID)
	}

	if len(urls) == 0 {
		return nil, nil
	}

	wanted := set(urls)

	// Each video's metadata, its date among them (last30days: "no --flat-playlist so dates are real").
	out, err := ytRun(ctx, run, r, youtubeSearchTimeout,
		append(append(slices.Clone(ytBase), "--dump-json", "--no-download", "--"), urls...)...)
	if err != nil {
		return nil, err
	}

	var videos []Item

	for line := range strings.SplitSeq(string(out), "\n") {
		var v ytVideo
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &v) != nil ||
			!wanted["https://www.youtube.com/watch?v="+v.ID] {
			continue
		}

		delete(wanted, "https://www.youtube.com/watch?v="+v.ID)
		videos = append(videos, v.item())
	}

	return videos, nil
}

func youtubeInWindow(videos []Item, r Request) []Item {
	var out []Item

	for _, v := range videos {
		if inDays(v.PublishedAt, r.From, r.To) {
			out = append(out, v)
		}
	}

	return out
}

// uploadFilter is YouTube's upload-date filter (the results page's `sp`), the smallest of this week, this month and
// this year that holds the window; "" for a window that ended more than 2 days ago or is longer (last30days
// _UPLOAD_DATE_FILTERS and _upload_date_filter).
func uploadFilter(r Request) string {
	if r.now().Sub(r.To) > 2*24*time.Hour {
		return ""
	}

	span := r.now().Sub(r.From).Hours() / 24

	for _, f := range []struct {
		days float64
		sp   string
	}{{7, "EgIIAw=="}, {31, "EgIIBA=="}, {366, "EgIIBQ=="}} {
		if span <= f.days {
			return f.sp
		}
	}

	return ""
}

func watched(it Item) float64 {
	return it.Engagement["views"] + it.Engagement["likes"] + it.Engagement["comments"]
}

func (v ytVideo) item() Item {
	it := Item{
		NativeID: v.ID, URL: "https://www.youtube.com/watch?v=" + v.ID, Title: v.Title, Text: v.Description,
		Author: cmp.Or(v.Channel, v.Uploader), Engagement: map[string]float64{},
	}

	if t, err := time.Parse("20060102", v.UploadDate); err == nil {
		it.PublishedAt = &t
	}

	for name, n := range map[string]*float64{"views": v.Views, "likes": v.Likes, "comments": v.Comments} {
		if n != nil {
			it.Engagement[name] = *n
		}
	}

	return it
}

// youtubeCommentsOf reads a video's top comments: top-level only, the channel's own left out (last30days
// _ytdlp_comments_result and AGENTS.md: "a comment by the item's own author … never community comments").
func youtubeCommentsOf(ctx context.Context, run Command, r Request, v Item) ([]Item, *SourceError) {
	ask := youtubeComments + 2

	out, err := ytRun(ctx, run, r, youtubeCommentTimeout, append(slices.Clone(ytBase), "--write-comments",
		"--skip-download", "--dump-single-json", "--extractor-args",
		fmt.Sprintf("youtube:comment_sort=top;max_comments=%d,%d,0,0", ask, ask), "--", v.URL)...)
	if err != nil {
		return nil, AsFailure(err)
	}

	var payload struct {
		ChannelID string `json:"channel_id"`
		Comments  []struct {
			ID        string   `json:"id"`
			Text      string   `json:"text"`
			Author    string   `json:"author"`
			AuthorID  string   `json:"author_id"`
			Likes     *float64 `json:"like_count"`
			Timestamp int64    `json:"timestamp"`
			Parent    string   `json:"parent"`
			Uploader  *bool    `json:"author_is_uploader"`
		} `json:"comments"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, Fail(CodeBroken, "yt-dlp's comments are not JSON: %s", err.Error())
	}

	var items []Item

	for _, c := range payload.Comments {
		own := c.Uploader != nil && *c.Uploader || c.Uploader == nil && payload.ChannelID != "" &&
			c.AuthorID == payload.ChannelID
		if c.Parent != "" && c.Parent != "root" || own || strings.TrimSpace(c.Text) == "" || c.ID == "" {
			continue
		}

		it := Item{
			NativeID: c.ID, URL: v.URL + "&lc=" + c.ID, Author: c.Author, Text: c.Text, Parent: v.NativeID,
			Container: v.Title,
		}

		if c.Likes != nil {
			it.Engagement = map[string]float64{"likes": *c.Likes}
		}

		if c.Timestamp > 0 {
			t := time.Unix(c.Timestamp, 0).UTC()
			it.PublishedAt = &t
		}

		items = append(items, it)

		if len(items) == youtubeComments {
			break
		}
	}

	return items, nil
}

// youtubeSaid reads a video's captions, uploaded or automatic, into a private directory, and gives the transcript's
// most quotable sentences; "" when the video has none (yt-dlp then writes no file: last30days "exit 0 with no file ==
// no matching captions").
func youtubeSaid(ctx context.Context, run Command, r Request, id, topic string) (string, *SourceError) {
	dir, err := os.MkdirTemp("", "listening-captions-")
	if err != nil {
		return "", Fail(CodeBroken, "%s", err.Error())
	}

	defer os.RemoveAll(dir) //nolint:errcheck // A temporary directory's removal has nothing to report.

	_, runErr := ytRun(ctx, run, r, youtubeCaptionTimeout, append(slices.Clone(ytBase), "--write-subs",
		"--write-auto-subs", "--sub-lang", youtubeLangs, "--sub-format", "vtt", "--skip-download", "-o",
		filepath.Join(dir, "%(id)s"), "--", "https://www.youtube.com/watch?v="+id)...)

	// A track written before a later language failed is still the transcript (last30days: "a partial success is
	// still a real transcript").
	vtt := pickCaptions(dir, id)
	if vtt == "" {
		if runErr != nil {
			return "", AsFailure(runErr)
		}

		return "", nil
	}

	return strings.Join(highlights(vttText(vtt), topic), " … "), nil
}

// pickCaptions is the caption file of the first language asked that was written.
func pickCaptions(dir, id string) string {
	for code := range strings.SplitSeq(youtubeLangs, ",") {
		if body, err := os.ReadFile(filepath.Join(dir, id+"."+code+".vtt")); err == nil {
			return string(body)
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, id+".*.vtt"))
	if len(matches) == 0 {
		return ""
	}

	body, _ := os.ReadFile(matches[0])

	return string(body)
}

var vttTiming = regexp.MustCompile(`^(?:\d{2,}:)?\d{2}:\d{2}\.\d{3}\s+-->\s+`)

// vttText is WebVTT as plain text: its header, timings and tags dropped, an automatic caption's repeated lines once
// (Agent-Reach youtube.vtt_to_text, last30days _clean_vtt).
func vttText(vtt string) string {
	var out []string

	seen := map[string]bool{}
	inCue := false

	for line := range strings.SplitSeq(vtt, "\n") {
		s := strings.TrimSpace(line)

		switch {
		case s == "":
			inCue = false
		case vttTiming.MatchString(s):
			inCue = true
		case inCue:
			s = stripHTML(s)
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}

	return strings.Join(out, " ")
}

var (
	sentenceEnd = regexp.MustCompile(`([.!?])\s+`)
	filler      = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(hey |hi |what's up|welcome back|in today's video|don't forget to)`),
		regexp.MustCompile(`(?i)(subscribe|like and comment|hit the bell|check out the link|down below)`),
		regexp.MustCompile(`(?i)^(so |and |but |okay |alright |um |uh )`),
		regexp.MustCompile(`(?i)(thanks for watching|see you (next|in the)|bye)`),
	}
	hasDigit   = regexp.MustCompile(`\d`)
	properNoun = regexp.MustCompile(`\p{Lu}\p{Ll}+`)
)

// highlights are a transcript's most quotable sentences, in the transcript's order of score: 8 to 50 words, not
// filler, scored by numbers, names, questions and the topic's words (last30days extract_transcript_highlights; an
// unpunctuated automatic caption is cut into 20-word pieces).
func highlights(transcript, topic string) []string {
	sentences := strings.Split(sentenceEnd.ReplaceAllString(transcript, "$1\n"), "\n")

	if words := strings.Fields(transcript); len(sentences) <= 1 && len(words) > 50 {
		sentences = sentences[:0]
		for i := 0; i < len(words); i += 20 {
			sentences = append(sentences, strings.Join(words[i:min(i+20, len(words))], " "))
		}
	}

	topicWords := strings.Fields(normalize(topic))

	type scored struct {
		score int
		text  string
	}

	var candidates []scored

	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if n := len(strings.Fields(s)); n < 8 || n > 50 ||
			slices.ContainsFunc(filler, func(re *regexp.Regexp) bool { return re.MatchString(s) }) {
			continue
		}

		score := 0
		if hasDigit.MatchString(s) {
			score += 2
		}

		if properNoun.MatchString(s) {
			score++
		}

		if strings.Contains(s, "?") {
			score++
		}

		low := normalize(s)
		if slices.ContainsFunc(topicWords, func(w string) bool { return len([]rune(w)) > 2 && strings.Contains(low, w) }) {
			score += 2
		}

		candidates = append(candidates, scored{score, s})
	}

	slices.SortStableFunc(candidates, func(a, b scored) int { return b.score - a.score })

	out := make([]string, 0, youtubeHighlights)
	for _, c := range candidates[:min(len(candidates), youtubeHighlights)] {
		out = append(out, c.text)
	}

	return out
}
