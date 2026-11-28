package muse

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
)

// fakePage stands for an account's muse.ai tab: each script is answered by
// what answer gives, and each step is kept by name. The browser's own steps
// are tested against a fake Chrome (browser/contexts_test.go); this one
// answers the scripts of scripts.go.
type fakePage struct {
	mu     sync.Mutex
	answer func(script string) (any, error)
	steps  []string
	// fail, when set, fails every step of that kind.
	fail map[string]error
}

func (f *fakePage) kept(step string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.steps = append(f.steps, step)

	return f.fail[step]
}

func (f *fakePage) Navigate(_ context.Context, address string) error {
	return f.kept("navigate " + address)
}

func (f *fakePage) Evaluate(_ context.Context, script string, out any) error {
	value, err := f.answer(script)
	if err != nil {
		return err
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return json.Unmarshal(raw, out)
}

func (f *fakePage) InsertText(context.Context, string) error { return f.kept("insert") }
func (f *fakePage) Click(context.Context, float64, float64) error {
	return f.kept("click")
}
func (f *fakePage) KeyEnter(context.Context) error { return f.kept("enter") }
func (f *fakePage) Cookies(context.Context) ([]browser.Cookie, error) {
	return []browser.Cookie{{Name: "hatch_sess", Value: "v", Domain: ".muse.ai"}}, nil
}

func (f *fakePage) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.steps...)
}

const aThread = "0b3c1f7e-2a4d-4c5e-9f60-718293a4b5c6"

var fastTimings = timings{
	poll: time.Millisecond, ready: 60 * time.Millisecond,
	sendConfirm: 5 * time.Millisecond, resend: 20 * time.Millisecond, sendFail: 60 * time.Millisecond,
	stable: 5 * time.Millisecond, firstToken: 80 * time.Millisecond, stall: 30 * time.Millisecond,
	undelivered: 20 * time.Millisecond, total: time.Second, threadID: 10 * time.Millisecond,
	wakePoll: time.Millisecond, wakeConfirm: 20 * time.Millisecond,
}

func newDriver(page Page) *driver {
	return &driver{page: page, t: fastTimings, now: time.Now, logger: slog.New(slog.DiscardHandler)}
}

// muse is a scripted muse.ai: a page that is ready, a router that works, and
// a conversation that answers once sent.
type museSite struct {
	mu sync.Mutex
	// path is where the page is.
	path string
	// routerWorks says whether router.push lands.
	routerWorks bool
	// readyAfter counts page reads before the page is ready (slow hydration).
	readyAfter int
	reads      int
	// before is the conversation before the send; after, what each read shows once sent.
	before chat
	after  []chat
	sent   bool
	shown  int
	// composerSticks keeps the prompt in the composer.
	composerSticks bool
	answerText     string
	links          []Link
	// messages, when set, are every answer in the thread once sent, each with its links: what a read from the
	// script's first answer shows (readAnswer's "from").
	messages []answerMessage
	auth     map[string]any
	session  map[string]any
	// woken are the machines a wake was asked for; awake, when set, is the session read after one.
	woken           []string
	awake           map[string]any
	usageText       string
	usageReads      int
	settingsMissing bool
}

func (m *museSite) eval(script string) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case script == pageState:
		m.reads++

		return pageInfo{
			Path: m.path, Host: "muse.ai", Ready: m.reads > m.readyAfter, Composer: true,
			Empty: m.path == "/thread/new" && !m.sent,
		}, nil
	case script == softNewThread:
		if m.routerWorks {
			m.newThread()
		}

		return true, nil
	case script == composerRect:
		return map[string]float64{"x": 10, "y": 20}, nil
	case strings.Contains(script, "getOwnPropertyDescriptor"):
		return 5, nil
	case script == clickSend:
		if !m.composerSticks {
			m.sent = true
		}

		return "clicked", nil
	case script == chatState:
		if !m.sent {
			c := m.before
			if m.composerSticks {
				c.Composer = "hello"
			}

			return c, nil
		}

		c := m.after[min(m.shown, len(m.after)-1)]
		m.shown++

		if c.Count > 0 && m.path == "/thread/new" {
			m.path = "/thread/" + aThread
		}

		return c, nil
	case strings.Contains(script, "'img, video, source'"):
		if m.messages != nil {
			return m.readFrom(script), nil
		}

		return map[string]any{"matched": "message-item", "text": m.answerText, "links": m.links,
			"media": []string{"https://muse.ai/files/a.png"}}, nil
	case script == threadPath:
		return m.path, nil
	case script == authCheck:
		return m.auth, nil
	case script == sessionCheck:
		if len(m.woken) > 0 && m.awake != nil {
			return m.awake, nil
		}

		return m.session, nil
	case strings.Contains(script, "/api/hatch/vm/wake"):
		m.woken = append(m.woken, script)

		return map[string]any{"status": 200}, nil
	case script == settingsEntry:
		m.usageReads++
		if m.settingsMissing {
			return json.RawMessage("null"), nil
		}

		return map[string]float64{"x": 1, "y": 2}, nil
	case script == settingsItem:
		return map[string]float64{"x": 1, "y": 2}, nil
	case script == dialogText:
		return m.usageText, nil
	}

	return nil, errors.New("an unknown script")
}

// answerMessage is one of Muse's messages: its text and links.
type answerMessage struct {
	text  string
	links []Link
}

var fromPattern = regexp.MustCompile(`const from = Math\.min\((\d+),`)

// readFrom answers readAnswer as the page would: the messages from the script's "from" on, joined, or the last
// message alone when the script names none.
func (m *museSite) readFrom(script string) map[string]any {
	from := len(m.messages) - 1
	if g := fromPattern.FindStringSubmatch(script); g != nil {
		n, _ := strconv.Atoi(g[1])
		from = min(n, len(m.messages)-1)
	}

	var (
		texts []string
		links []Link
	)

	for _, msg := range m.messages[from:] {
		texts = append(texts, msg.text)
		links = append(links, msg.links...)
	}

	return map[string]any{"matched": "message-item", "text": strings.Join(texts, "\n\n"), "links": links,
		"media": []string{}}
}

// newThread is a blank thread: nothing sent in it yet.
func (m *museSite) newThread() {
	m.path, m.sent, m.shown = "/thread/new", false, 0
}

func answered(text string) []chat {
	return []chat{{Count: 1, Last: text[:3], Busy: true}, {Count: 1, Last: text, Busy: true}, {Count: 1, Last: text}}
}

func site() *museSite {
	return &museSite{path: "/", routerWorks: true, answerText: "The answer [1]", after: answered("The answer [1]"),
		links: []Link{{URL: "https://example.com/a", Text: "a source"}}}
}

func deadline() time.Time { return time.Now().Add(fastTimings.total) }

// A new thread is opened through the page's router, the prompt set and sent,
// and the answer read with its links, media and thread once it stops changing.
func TestAQuestionIsAskedInANewThreadAndAnswered(t *testing.T) {
	t.Parallel()

	m := site()
	page := &fakePage{answer: m.eval}

	a, sent, err := newDriver(page).ask(t.Context(), "hello", "", deadline())
	require.NoError(t, err)
	assert.True(t, sent)
	assert.Equal(t, "The answer [1]", a.Text)
	assert.Equal(t, []Link{{URL: "https://example.com/a", Text: "a source"}}, a.Links)
	assert.Equal(t, []string{"https://muse.ai/files/a.png"}, a.Media)
	assert.Equal(t, aThread, a.Thread)
	assert.Equal(t, []string{"click"}, page.took(), "the composer is clicked; nothing is navigated")
}

// A router that does not reach /thread/new is followed by one navigation.
func TestTheRouterNotReachingANewThreadNavigates(t *testing.T) {
	t.Parallel()

	m := site()
	m.routerWorks = false
	page := &fakePage{answer: func(script string) (any, error) { return m.eval(script) }}
	page.fail = map[string]error{}

	// The navigation lands where it was sent.
	nav := &navigating{fakePage: page, site: m}

	_, _, err := newDriver(nav).ask(t.Context(), "hello", "", deadline())
	require.NoError(t, err)
	assert.Contains(t, page.took(), "navigate https://muse.ai/thread/new")
}

type navigating struct {
	*fakePage
	site *museSite
}

func (n *navigating) Navigate(ctx context.Context, address string) error {
	n.site.mu.Lock()
	if n.site.path = strings.TrimPrefix(address, Site); n.site.path == "/thread/new" {
		n.site.newThread()
	}
	n.site.mu.Unlock()

	return n.fakePage.Navigate(ctx, address)
}

// In a continued thread the answers already shown are the baseline: the old
// last answer is not taken for the new one.
func TestAnOldAnswerInAContinuedThreadIsNotTheNewOne(t *testing.T) {
	t.Parallel()

	m := site()
	m.path = "/thread/" + aThread
	m.before = chat{Count: 2, Last: "the old answer"}
	m.after = []chat{
		{Count: 2, Last: "the old answer"}, {Count: 2, Last: "the old answer", Busy: true},
		{Count: 3, Last: "The new", Busy: true}, {Count: 3, Last: "The new answer"},
	}
	m.answerText = "The new answer"
	page := &navigating{fakePage: &fakePage{answer: m.eval}, site: m}

	a, _, err := newDriver(page).ask(t.Context(), "and then?", aThread, deadline())
	require.NoError(t, err)
	assert.Equal(t, "The new answer", a.Text)
	assert.Contains(t, page.took(), "navigate https://muse.ai/thread/"+aThread)
}

// A prompt that stays in the composer is sent once more, then fails; it
// never reached Muse.
//
// It runs in a synctest bubble, on its fake clock: on the wall clock a poll
// that a loaded -race runner paused from before the resend (20 ms) to past
// the failure (60 ms) went straight to the failure, with one click (#984).
func TestASendThatFailsIsTriedOnceMoreThenFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		m := site()
		m.composerSticks = true

		var clicks int

		page := &fakePage{answer: func(script string) (any, error) {
			if script == clickSend {
				clicks++
			}

			return m.eval(script)
		}}

		_, sent, err := newDriver(page).ask(t.Context(), "hello", "", deadline())
		require.ErrorIs(t, err, ErrSendFailed)
		assert.False(t, sent)
		assert.Equal(t, 2, clicks, "Send is clicked once more, and no more")
	})
}

// A Stop that never ends meets the total deadline; the result is unknown.
func TestAnAnswerThatNeverEndsMeetsTheDeadline(t *testing.T) {
	t.Parallel()

	m := site()
	m.after = []chat{{Count: 1, Last: "still", Busy: true}}

	_, sent, err := newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "hello", "",
		time.Now().Add(50*time.Millisecond))
	require.ErrorIs(t, err, ErrUnknown)
	assert.True(t, sent)
}

// The caller's canceling ends the wait; what was sent is unknown.
func TestTheCallerCancelling(t *testing.T) {
	t.Parallel()

	m := site()
	m.after = []chat{{Count: 1, Last: "still", Busy: true}}

	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()

	_, sent, err := newDriver(&fakePage{answer: m.eval}).ask(ctx, "hello", "", deadline())
	require.ErrorIs(t, err, ErrUnknown)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, sent)
}

// Chrome dying after the send: the call says it was sent and its result is
// lost with the tab.
func TestChromeDyingAfterTheSend(t *testing.T) {
	t.Parallel()

	m := site()
	page := &fakePage{answer: func(script string) (any, error) {
		m.mu.Lock()
		sent := m.sent
		m.mu.Unlock()

		if sent && script == chatState {
			return nil, browser.ErrPageGone
		}

		return m.eval(script)
	}}

	_, sent, err := newDriver(page).ask(t.Context(), "hello", "", deadline())
	require.ErrorIs(t, err, browser.ErrPageGone)
	assert.True(t, sent)
}

// The owner taking the browser while sending or while waiting interrupts the
// question; before the press nothing was sent.
func TestTheOwnerTakingTheBrowserInterrupts(t *testing.T) {
	t.Parallel()

	for _, at := range []string{"setter", "wait"} {
		m := site()
		page := &fakePage{answer: func(script string) (any, error) {
			m.mu.Lock()
			sent := m.sent
			m.mu.Unlock()

			if at == "setter" && strings.Contains(script, "getOwnPropertyDescriptor") ||
				at == "wait" && sent && script == chatState {
				return nil, browser.ErrHeld
			}

			return m.eval(script)
		}}

		_, sent, err := newDriver(page).ask(t.Context(), "hello", "", deadline())
		require.ErrorIs(t, err, ErrInterrupted, at)
		assert.Equal(t, at == "wait", sent, at)
	}
}

// Muse's permission card stops the question with its text; nothing clicks it.
func TestAPermissionCardStopsTheQuestion(t *testing.T) {
	t.Parallel()

	m := site()
	m.after = []chat{{Count: 1, Last: "I will upload", Busy: true, Approval: "Allow Muse to publish a link?"}}
	page := &fakePage{answer: m.eval}

	a, sent, err := newDriver(page).ask(t.Context(), "hello", "", deadline())
	require.ErrorIs(t, err, ErrAsksPermission)
	assert.True(t, sent)
	assert.Equal(t, "Allow Muse to publish a link?", a.Approval)
	assert.Equal(t, []string{"click"}, page.took(), "only the composer was clicked")
}

// A quota hint counts inside the finished reply only.
func TestAQuotaHintCountsInTheReplyOnly(t *testing.T) {
	t.Parallel()

	m := site()
	m.answerText = "Sorry, you have reached your weekly limit. Try again later."
	m.after = answered(m.answerText)

	_, _, err := newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "hello", "", deadline())
	require.ErrorIs(t, err, ErrQuota)

	// The page's own text (a thread's title in the sidebar, the prompt) is not read for it.
	m = site()
	m.before = chat{Composer: ""}
	_, _, err = newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "what is a usage limit?", "", deadline())
	require.NoError(t, err)

	assert.Empty(t, quotaHint("No limit here."))
}

// A stall note with no output fails after its time; Muse's error notice ends
// the turn with its text; a lingering delivery note fails.
func TestStallsErrorsAndDelivery(t *testing.T) {
	t.Parallel()

	m := site()
	m.after = []chat{{Stalled: true}}
	_, _, err := newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "hello", "", deadline())
	require.ErrorIs(t, err, ErrUnknown)
	assert.Contains(t, err.Error(), "connecting")

	m = site()
	m.after = []chat{{Error: "Something went wrong"}}
	a, _, err := newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "hello", "", deadline())
	require.ErrorIs(t, err, ErrAnswerError)
	assert.Equal(t, "Something went wrong", a.Error)

	m = site()
	m.after = []chat{{Count: 1, Last: "partial", Busy: true, Undelivered: true}}
	_, _, err = newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "hello", "", deadline())
	require.ErrorIs(t, err, ErrUnknown)
	assert.Contains(t, err.Error(), "delivery")
}

// A page that hydrates slowly is waited for.
func TestSlowHydrationIsWaitedFor(t *testing.T) {
	t.Parallel()

	m := site()
	m.readyAfter = 10

	_, _, err := newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "hello", "", deadline())
	require.NoError(t, err)

	m = site()
	m.readyAfter = 1 << 30
	_, sent, err := newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "hello", "", deadline())
	require.ErrorIs(t, err, ErrPageNotReady)
	assert.False(t, sent)
}

// The account's check: 401 needs a sign-in, 403 is refused, a session not
// assigned is a workspace not ready; the machine's state never refuses it.
func TestTheAccountsCheck(t *testing.T) {
	t.Parallel()

	signed := map[string]any{"status": 200, "ok": true, "viewer": "v1"}

	for _, c := range []struct {
		auth, session map[string]any
		want          error
	}{
		{map[string]any{"status": 401}, nil, ErrNeedsSignIn},
		{map[string]any{"status": 403}, nil, ErrRefused},
		{signed, map[string]any{"status": 200, "session": "pending", "vmId": "vm1", "vm": "RUNNING"}, ErrWorkspace},
		{signed, map[string]any{"status": 200, "session": "assigned", "vm": "STOPPED"}, nil},
		{signed, map[string]any{"status": 200, "session": "assigned", "vm": "RUNNING"}, nil},
	} {
		m := site()
		m.auth, m.session = c.auth, c.session

		viewer, _, err := newDriver(&fakePage{answer: m.eval}).checkAccount(t.Context())
		if c.want != nil {
			require.ErrorIs(t, err, c.want)

			continue
		}

		require.NoError(t, err)
		assert.Equal(t, "v1", viewer)
	}
}

// A machine that is not running is woken (issue #810), as www's
// upstream/muse.py:121-141 does: the wake names it, and the account is used
// whether or not it is then seen running. One with no id, running or
// disabled is not woken.
func TestTheAccountsMachineIsWokenAndNeverRefusesIt(t *testing.T) {
	t.Parallel()

	signed := map[string]any{"status": 200, "ok": true, "viewer": "v1"}
	running := map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "RUNNING"}

	for _, c := range []struct {
		name           string
		session, awake map[string]any
		woken          bool
		note           string
	}{
		{"unknown, then running", map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "UNKNOWN"},
			running, true, ""},
		{"woken, not confirmed", map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "STOPPED"},
			map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "STARTING"}, true,
			"its machine was woken and is starting, not seen running"},
		{"woken, no read answered", map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "UNKNOWN"},
			map[string]any{"status": 0}, true, "its machine was woken and is unknown, not seen running"},
		{"no machine id", map[string]any{"status": 200, "session": "assigned", "vm": "UNKNOWN"}, running, false, ""},
		{"running", running, running, false, ""},
		{"disabled", map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "DISABLED"}, running,
			false, "its machine is disabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			m := site()
			m.auth, m.session, m.awake = signed, c.session, c.awake

			viewer, note, err := newDriver(&fakePage{answer: m.eval}).checkAccount(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "v1", viewer)
			assert.Equal(t, c.note, note)

			if !c.woken {
				assert.Empty(t, m.woken)

				return
			}

			require.Len(t, m.woken, 1)
			assert.Contains(t, m.woken[0], `vm_id: "vm1", retry_count: 0`)
		})
	}
}

// Settings → Usage is read and parsed; the page goes back to a new thread.
func TestUsageIsReadFromSettings(t *testing.T) {
	t.Parallel()

	m := site()
	m.usageText = "Account\nUsage\nFree plan\nWeekly limit resets on Sep 30\n62% used\nAdditional tokens\n" +
		"Never expires\n0% used (2B tokens left)\nLanguage\nEnglish"
	page := &navigating{fakePage: &fakePage{answer: m.eval}, site: m}

	u, err := newDriver(page).usage(t.Context())
	require.NoError(t, err)
	assert.Equal(t,
		Usage{Plan: "Free plan", UsedPercent: 62, Reset: "Sep 30", ExtraLeft: "2B tokens left", Found: true}, u)
	assert.Equal(t, "62% used · resets Sep 30", u.Text())
	assert.True(t, u.Room())
}

// The reset is kept as Muse writes it: missing, in another year, in another
// time zone; nothing is made of it.
func TestTheResetIsKeptAsText(t *testing.T) {
	t.Parallel()

	missing := ParseUsage("Usage\nFree plan\n100% used\nLanguage")
	assert.True(t, missing.Found)
	assert.False(t, missing.Room())
	assert.Equal(t, "100% used · resets unknown", missing.Text())

	assert.Equal(t, "Jan 2, 2027", ParseUsage("Usage\nWeekly limit resets on Jan 2, 2027\n5% used").Reset)
	assert.Equal(t, "Sep 30 at 5:00 PM PDT",
		ParseUsage("Usage\nWeekly limit resets on Sep 30 at 5:00 PM PDT\n5% used").Reset)

	none := ParseUsage("Usage\nFree plan\nLanguage")
	assert.False(t, none.Found, "a plan alone is no reading")
	assert.Equal(t, "unknown", none.Text())
}

func TestParseUsageFromTheVietnameseMuseDialog(t *testing.T) {
	t.Parallel()

	// Muse on zm, 2026-11-27, with account values replaced.
	text := "Cài đặt\n[account details omitted]\nMức sử dụng\nGói miễn phí\n" +
		"Giới hạn hàng tuần sẽ đặt lại vào 12 tháng 1\nĐã dùng 6.5%\nToken bổ sung\n" +
		"Không bao giờ hết hạn\nĐã dùng 0% (còn 2\u00a0tỷ token)\nNâng cấp\nNgôn ngữ"
	assert.Equal(t, Usage{Plan: "Gói miễn phí", UsedPercent: 6.5, Reset: "12 tháng 1",
		ExtraLeft: "còn 2 tỷ token", Found: true}, ParseUsage(text))
}

func TestThreadIDs(t *testing.T) {
	t.Parallel()

	assert.Equal(t, aThread, ThreadID("/thread/"+aThread))
	assert.Equal(t, aThread, ThreadID("https://muse.ai/thread/"+strings.ToUpper(aThread)))
	assert.Equal(t, aThread, ThreadID(aThread))
	assert.Empty(t, ThreadID("/thread/new"))
	assert.Empty(t, ThreadID("/thread/x/../../etc"))
}

// An answer Muse gives in several messages, its list and then a link card, is read whole: every message after those
// the thread showed before the send, never the last one alone (owner, 2026-11-28: a Threads list ended with a link
// card, and only the card was read).
func TestAnAnswerInSeveralMessagesIsReadWhole(t *testing.T) {
	t.Parallel()

	m := site()
	m.path = "/thread/" + aThread
	m.before = chat{Count: 2, Last: "an earlier answer"}
	m.after = []chat{{Count: 4, Last: "TH threads.com", Busy: true}, {Count: 4, Last: "TH threads.com"}}
	card := Link{URL: "https://l.meta.ai/?u=https%3A%2F%2Fwww.threads.com%2F%40a%2Fpost%2FDfe40MVAZjj", Text: "TH"}
	m.messages = []answerMessage{
		{text: "an older answer"},
		{text: "an earlier answer", links: []Link{{URL: "https://example.com/old"}}},
		{text: "post: 1\nlink: Bài đăng\nquote: Vào web đọc truyện", links: []Link{card}},
		{text: "TH threads.com", links: []Link{card}},
	}

	a, _, err := newDriver(&fakePage{answer: m.eval}).ask(t.Context(), "Continue", aThread, deadline())
	require.NoError(t, err)
	assert.Equal(t, "post: 1\nlink: Bài đăng\nquote: Vào web đọc truyện\n\nTH threads.com", a.Text)
	assert.Equal(t, []Link{card, card}, a.Links, "the links of every new message, in order; the fake does not dedupe")
}
