package listening_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 3 of PR #1321: each finding's probe, failing before its fix.

// hostsTo is a client whose requests for named hosts go to test servers, as the real hosts would answer.
func hostsTo(servers map[string]string) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		base, ok := servers[r.URL.Hostname()]
		if !ok {
			return nil, &url.Error{Op: "dial", URL: r.URL.String(), Err: io.ErrUnexpectedEOF}
		}

		to, _ := url.Parse(base)
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = to.Scheme, to.Host, ""

		return http.DefaultTransport.RoundTrip(out)
	})}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// P1-1: Otofun's old host's robots.txt redirects to the new one's, which answers 429: the rest is the new host's,
// whichever host a thread link names, as readPage rests each hop's host (careers.go readPage).
func TestRoundThreeOtofunRobotsRedirectSharesTheRateLimit(t *testing.T) {
	t.Parallel()

	for _, order := range [][2]string{{"otofun.net", "www.otofun.net.vn"}, {"www.otofun.net.vn", "otofun.net"}} {
		var (
			mu     sync.Mutex
			robots int
		)

		newHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				mu.Lock()
				robots++
				mu.Unlock()

				w.Header().Set("Retry-After", "7200")
				w.WriteHeader(http.StatusTooManyRequests)

				return
			}

			_, _ = io.WriteString(w, "thread")
		}))
		t.Cleanup(newHost.Close)

		oldHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://www.otofun.net.vn/robots.txt", http.StatusMovedPermanently)
		}))
		t.Cleanup(oldHost.Close)

		c := newClock()
		s := listening.New(listening.Config{
			Platforms: listening.Sources(listening.Endpoints{
				Pages: hostsTo(map[string]string{"otofun.net": oldHost.URL, "www.otofun.net.vn": newHost.URL}),
			}, http.DefaultClient),
			Now: c.Now, Sleep: noSleep,
		})

		for _, host := range order {
			web := func(context.Context, string, int) (websearch.Answer, error) {
				return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{
					{Title: "VETC", URL: "https://" + host + "/threads/vetc.1971431/", Snippet: "VETC trừ tiền"},
				}}, nil
			}

			a, err := s.Search(t.Context(), plan([]string{listening.SourceOtofun}, "VETC"), listening.Caller{Web: web})
			require.NoError(t, err)
			assert.Equal(t, listening.CodeRateLimited, coverageOf(t, a, listening.SourceOtofun).Code, host)

			c.Advance(30 * time.Minute)
		}

		mu.Lock()
		assert.Equal(t, 1, robots, "%v: the new host is asked once in its rest", order)
		mu.Unlock()
	}
}

// P1-2: a robots.txt 429 whose Retry-After is a week, as a date or in seconds, keeps that week (RFC 9110 §10.2.3).
func TestRoundThreeRobotsLongRetryAfter(t *testing.T) {
	t.Parallel()

	for _, after := range []string{start.AddDate(0, 0, 7).UTC().Format(http.TimeFormat), "604800"} {
		forum, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				w.Header().Set("Retry-After", after)
				w.WriteHeader(http.StatusTooManyRequests)

				return
			}

			_, _ = io.WriteString(w, "thread")
		})

		web := func(context.Context, string, int) (websearch.Answer, error) {
			return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{
				{Title: "VETC", URL: base + "/threads/vetc.1971431/", Snippet: "VETC trừ tiền"},
			}}, nil
		}

		c := newClock()
		s := listening.New(listening.Config{
			Platforms: listening.Sources(listening.Endpoints{ForumURLs: map[string]string{listening.SourceOtofun: base}},
				http.DefaultClient),
			Now: c.Now, Sleep: noSleep,
		})
		p := plan([]string{listening.SourceOtofun}, "VETC")

		a, err := s.Search(t.Context(), p, listening.Caller{Web: web})
		require.NoError(t, err)
		assert.Equal(t, start.AddDate(0, 0, 7).UTC().Format(time.RFC3339),
			coverageOf(t, a, listening.SourceOtofun).Streams[0].Tried[0].Until, after)

		c.Advance(90 * time.Minute)

		_, err = s.Search(t.Context(), p, listening.Caller{Web: web})
		require.NoError(t, err)
		assert.Equal(t, 1, requestsTo(forum, "/robots.txt"), after)
	}
}

// P2: yt-dlp that prints one video's metadata, then fails 429 on the next, has met the block: the search keeps the
// video, reads no captions or comments, asks no recent uploads, and the backend rests (last30days
// youtube_yt.py:1049-1054 and 967-972: the block is recorded even when stdout has videos).
func TestRoundThreeYouTubeBlockWithPartialOutput(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		runs [][]string
	)

	run := func(_ context.Context, args ...string) ([]byte, string, error) {
		mu.Lock()
		runs = append(runs, args)
		mu.Unlock()

		switch {
		case slices.Contains(args, "--flat-playlist"):
			return []byte("{\"id\": \"new1\", \"ie_key\": \"Youtube\"}\n{\"id\": \"new2\", \"ie_key\": \"Youtube\"}"), "", nil
		case slices.Contains(args, "--no-download"):
			return []byte(video("new1", "VETC tháng này", 2, 500, 10, 2)),
				"ERROR: [youtube] new2: HTTP Error 429: Too Many Requests", &exec.ExitError{}
		default:
			return []byte(`{"comments": []}`), "", nil
		}
	}

	s := keyless(newClock(), listening.Endpoints{YtDlp: run})

	a := one(t, s, listening.SourceYouTube, "VETC")

	cov := coverageOf(t, a, listening.SourceYouTube)
	assert.Equal(t, listening.StatePartial, cov.State)
	assert.Equal(t, listening.CodeRateLimited, cov.Code)
	assert.Contains(t, byNative(a.Items), "new1", "the video printed before the block is kept")

	mu.Lock()
	first := len(runs)
	assert.Equal(t, 2, first, "only the listing and the metadata read: no recent uploads, captions or comments")
	mu.Unlock()

	one(t, s, listening.SourceYouTube, "VETC")

	mu.Lock()
	defer mu.Unlock()

	assert.Len(t, runs, first, "the backend rests: the next search runs no yt-dlp")
}

// Review round 4 of PR #1321: a block met after yt-dlp printed something is still the stream's failure and rests the
// backend when the window leaves no video, or when only a channel was printed: not_read with its rest, and the next
// search runs no yt-dlp (registry.go's failure path; last30days youtube_yt.py:1049-1054).
func TestRoundFourYouTubeBlockWithNothingLeftRests(t *testing.T) {
	t.Parallel()

	for name, printed := range map[string]map[string]string{
		"an old video": {
			"list": `{"id": "old1", "ie_key": "Youtube"}` + "\n" + `{"id": "old2", "ie_key": "Youtube"}`,
			"read": video("old1", "VETC năm ngoái", 60, 500, 10, 2),
		},
		"only a channel": {"list": `{"id": "UCvetc", "ie_key": "YoutubeTab", "_type": "url"}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				mu   sync.Mutex
				runs int
			)

			run := func(_ context.Context, args ...string) ([]byte, string, error) {
				mu.Lock()
				runs++
				mu.Unlock()

				blocked := "ERROR: [youtube] old2: HTTP Error 429: Too Many Requests"

				switch {
				case slices.Contains(args, "--flat-playlist") && printed["read"] == "":
					return []byte(printed["list"]), blocked, &exec.ExitError{}
				case slices.Contains(args, "--flat-playlist"):
					return []byte(printed["list"]), "", nil
				case slices.Contains(args, "--no-download"):
					return []byte(printed["read"]), blocked, &exec.ExitError{}
				default:
					return []byte(`{"comments": []}`), "", nil
				}
			}

			s := keyless(newClock(), listening.Endpoints{YtDlp: run})

			a := one(t, s, listening.SourceYouTube, "VETC")

			cov := coverageOf(t, a, listening.SourceYouTube)
			assert.Equal(t, listening.StateNotRead, cov.State)
			assert.Equal(t, listening.CodeRateLimited, cov.Code)
			assert.NotEmpty(t, cov.Streams[0].Tried[0].Until, "the backend rests")

			mu.Lock()
			first := runs
			mu.Unlock()

			one(t, s, listening.SourceYouTube, "VETC")

			mu.Lock()
			defer mu.Unlock()

			assert.Equal(t, first, runs, "the next search runs no yt-dlp")
		})
	}
}
