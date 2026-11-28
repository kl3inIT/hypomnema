package muse

// The driver: one question to muse.ai in an account's tab (issue #782, rows
// 10-16). It opens a new thread or the one asked for, takes the answers
// already shown as a baseline, sends the prompt, waits for the new answer to
// end, and reads its text, links and media. Muse's own permission cards stop
// it and are never clicked. Every step is a step of the browser's tab, under
// the owner's hold (browser/contexts.go). The timings are the references':
// www222fff/muse2api (drivers/base.py:30-40, driver.py:61), czg86389-hub's
// (engine.py:1350-1367) and Muse-Chat-MCP's (muse-driver.mjs:403-420).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
)

// Site is Muse's address.
const Site = "https://muse.ai"

// Page is an account's tab, as the browser engine gives it.
type Page interface {
	Navigate(ctx context.Context, address string) error
	Evaluate(ctx context.Context, expression string, result any) error
	InsertText(ctx context.Context, text string) error
	Click(ctx context.Context, x, y float64) error
	KeyEnter(ctx context.Context) error
	Cookies(ctx context.Context) ([]browser.Cookie, error)
}

// Why a question did not get its answer. Each is the cause observed; the
// pool keeps it as the account's state where it is one (row 7).
var (
	ErrNeedsSignIn    = errors.New("the Muse account needs to be signed in again")
	ErrRefused        = errors.New("muse.ai refused the account (403: region, network or access)")
	ErrWorkspace      = errors.New("the account's Muse workspace is not ready")
	ErrPageNotReady   = errors.New("muse.ai's page did not become ready")
	ErrQuota          = errors.New("the Muse account is out of quota")
	ErrSendFailed     = errors.New("the prompt could not be sent")
	ErrInterrupted    = errors.New("interrupted by the owner, who took the browser")
	ErrUnknown        = errors.New("the prompt was sent and its result is unknown")
	ErrAsksPermission = errors.New("muse asks for permission")
	ErrAnswerError    = errors.New("muse answered with an error")
)

// timings are how long each wait lasts.
type timings struct {
	poll time.Duration
	// ready bounds a new or opened thread's wait for its composer.
	ready time.Duration
	// sendConfirm is how long a send is watched before it is taken as not sent; resend when the prompt is still
	// in the composer with no answer; sendFail when the send fails.
	sendConfirm, resend, sendFail time.Duration
	// stable is how long a finished answer must stay unchanged.
	stable time.Duration
	// firstToken bounds the first output; stall a stall note's life; undelivered the delivery note's.
	firstToken, stall, undelivered time.Duration
	// total bounds a whole question unless the caller gives another.
	total time.Duration
	// threadID is how long the thread's address is waited for.
	threadID time.Duration
	// wakePoll is how often a woken machine's state is read; wakeConfirm how long it is waited for.
	wakePoll, wakeConfirm time.Duration
}

var defaultTimings = timings{
	poll: 300 * time.Millisecond, ready: 30 * time.Second,
	sendConfirm: 3 * time.Second, resend: 25 * time.Second, sendFail: 60 * time.Second,
	stable: 1200 * time.Millisecond, firstToken: 75 * time.Second, stall: 45 * time.Second,
	undelivered: 30 * time.Second, total: 300 * time.Second, threadID: 2 * time.Second,
	// www's upstream/muse.py:149-170 waits 20 s for RUNNING.
	wakePoll: 2 * time.Second, wakeConfirm: 20 * time.Second,
}

// MaxDeadline bounds the deadline a caller may give a question: within the
// seven minutes the tools server waits for a call (toolsmcp.CallTimeout).
const MaxDeadline = 6 * time.Minute

// driver asks muse.ai in one tab.
type driver struct {
	page   Page
	t      timings
	now    func() time.Time
	logger *slog.Logger
}

// Link is a link inside an answer.
type Link struct {
	URL  string `json:"url"`
	Text string `json:"text,omitempty"`
}

// answer is what the driver read.
type answer struct {
	Text  string
	Links []Link
	// Media are the addresses of the images and videos inside it, https or blob.
	Media []string
	// Thread is the Muse thread's id; empty when its address did not show.
	Thread string
	// Approval is a permission card's text, when Muse asks.
	Approval string
	// Error is Muse's error notice.
	Error string
}

var threadPattern = regexp.MustCompile(
	`^/thread/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})/?$`)

// ThreadID is the id of a thread's path or address; empty when it is none.
func ThreadID(s string) string {
	s = strings.TrimPrefix(strings.TrimPrefix(s, Site), "https://www.muse.ai")
	if m := threadPattern.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1])
	}

	if m := threadPattern.FindStringSubmatch("/thread/" + s); m != nil {
		return strings.ToLower(m[1])
	}

	return ""
}

// ThreadURL is a thread's address, for the owner's own Muse.
func ThreadURL(thread string) string { return Site + "/thread/" + thread }

func (d *driver) sleep(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// eval runs a script; the owner's hold is said as ErrInterrupted.
func (d *driver) eval(ctx context.Context, script string, out any) error {
	err := d.page.Evaluate(ctx, script, out)
	if errors.Is(err, browser.ErrHeld) {
		return fmt.Errorf("%w: %w", ErrInterrupted, err)
	}

	return err
}

func held(err error) error {
	if errors.Is(err, browser.ErrHeld) {
		return fmt.Errorf("%w: %w", ErrInterrupted, err)
	}

	return err
}

type pageInfo struct {
	Path     string `json:"path"`
	Host     string `json:"host"`
	Ready    bool   `json:"ready"`
	Composer bool   `json:"composer"`
	Empty    bool   `json:"empty"`
	Login    bool   `json:"login"`
}

// session is what /api/session says of the account's workspace.
type session struct {
	Status  int    `json:"status"`
	Session string `json:"session"`
	VMID    string `json:"vmId"`
	VM      string `json:"vm"`
}

// checkAccount asks muse.ai whether the tab is signed in and its workspace
// assigned: ErrNeedsSignIn on 401, ErrRefused on 403, ErrWorkspace when the
// session is not assigned. A machine that is not running is woken and never
// refuses the account (issue #810). It answers the account's Muse id, and a
// note on its machine when it was not seen running.
func (d *driver) checkAccount(ctx context.Context) (viewer, note string, err error) {
	var info pageInfo
	if err := d.eval(ctx, pageState, &info); err != nil {
		return "", "", err
	}

	// The checks are fetched from muse.ai's own origin.
	if !strings.HasSuffix(info.Host, "muse.ai") {
		if err := held(d.page.Navigate(ctx, Site+"/thread/new")); err != nil {
			return "", "", err
		}

		if err := d.untilLoaded(ctx); err != nil {
			return "", "", err
		}
	}

	var auth struct {
		Status int    `json:"status"`
		OK     bool   `json:"ok"`
		Viewer string `json:"viewer"`
		Error  string `json:"error"`
	}

	if err := d.eval(ctx, authCheck, &auth); err != nil {
		return "", "", err
	}

	switch {
	case auth.Status == 401 || (auth.Status == 200 && !auth.OK):
		return "", "", ErrNeedsSignIn
	case auth.Status == 403:
		return "", "", ErrRefused
	case !auth.OK:
		return "", "", fmt.Errorf("%w: the sign-in check answered %d %s", ErrPageNotReady, auth.Status, auth.Error)
	}

	var s session
	if err := d.eval(ctx, sessionCheck, &s); err != nil {
		return "", "", err
	}

	switch {
	case s.Status == 401:
		return "", "", ErrNeedsSignIn
	case s.Status == 403:
		return "", "", ErrRefused
	case s.Session != "assigned":
		return "", "", fmt.Errorf("%w: its session is %q", ErrWorkspace, orUnknown(s.Session))
	}

	note, err = d.wake(ctx, s)

	return auth.Viewer, note, err
}

// wake wakes the account's machine when it is not running, as both
// references do before a question (www's upstream/muse.py:121-141, czg's
// engine.py:607-650): POST /api/hatch/vm/wake, then /api/session read every
// wakePoll until RUNNING or wakeConfirm passes. The wake's own status says
// little (403 for a machine already running), and a machine not confirmed
// running does not refuse the account: the question is still asked. A
// DISABLED machine is not woken, as neither reference wakes it. It answers a
// note on the machine when it was not seen running.
func (d *driver) wake(ctx context.Context, s session) (string, error) {
	state := strings.ToUpper(s.VM)

	switch {
	case state == "RUNNING":
		return "", nil
	case state == "DISABLED":
		d.logger.WarnContext(ctx, "the Muse account's machine is disabled; asking anyway",
			slog.String("event", "muse.vm_disabled"))

		return "its machine is disabled", nil
	case s.VMID == "":
		d.logger.InfoContext(ctx, "muse.ai named no machine to wake; asking anyway",
			slog.String("event", "muse.vm_unknown"), slog.String("vm_state", orUnknown(s.VM)))

		return "", nil
	}

	var woke struct {
		Status int    `json:"status"`
		Error  string `json:"error"`
	}

	if err := d.eval(ctx, vmWake(s.VMID), &woke); err != nil {
		return "", err
	}

	end := d.now().Add(d.t.wakeConfirm)
	seen := ""

	for {
		if err := d.sleep(ctx, d.t.wakePoll); err != nil {
			return "", err
		}

		var now session
		if err := d.eval(ctx, sessionCheck, &now); err != nil {
			return "", err
		}

		// A read that did not answer costs only that read: unknown is not a machine down.
		if now.Status == 200 && now.VM != "" {
			seen = strings.ToUpper(now.VM)
		}

		if seen == "RUNNING" || d.now().After(end) {
			break
		}
	}

	d.logger.InfoContext(ctx, "the Muse account's machine was woken", slog.String("event", "muse.vm_wake"),
		slog.String("vm_state", orUnknown(s.VM)), slog.Int("wake_status", woke.Status),
		slog.String("vm_state_after", orUnknown(seen)))

	if seen == "RUNNING" {
		return "", nil
	}

	return fmt.Sprintf("its machine was woken and is %s, not seen running", strings.ToLower(orUnknown(seen))), nil
}

// untilLoaded waits for a navigation to land on muse.ai.
func (d *driver) untilLoaded(ctx context.Context) error {
	end := d.now().Add(d.t.ready)

	for {
		var info pageInfo
		if err := d.eval(ctx, pageState, &info); err != nil {
			return err
		}

		if strings.HasSuffix(info.Host, "muse.ai") && (info.Composer || info.Login) {
			if info.Login {
				return ErrNeedsSignIn
			}

			return nil
		}

		if d.now().After(end) {
			return fmt.Errorf("%w: muse.ai did not load", ErrPageNotReady)
		}

		if err := d.sleep(ctx, d.t.poll); err != nil {
			return err
		}
	}
}

// newThread opens a blank thread (row 10): through the page's router, else
// by navigating; ready when the path is /thread/new, the thread empty and the
// composer ready.
func (d *driver) newThread(ctx context.Context) error {
	var pushed bool
	if err := d.eval(ctx, softNewThread, &pushed); err != nil {
		return err
	}

	if !pushed {
		if err := held(d.page.Navigate(ctx, Site+"/thread/new")); err != nil {
			return err
		}
	}

	err := d.untilReady(ctx, func(info pageInfo) bool { return info.Path == "/thread/new" && info.Empty })
	if errors.Is(err, ErrPageNotReady) && pushed {
		// The router did not get there: a navigation does, once.
		d.logger.InfoContext(ctx, "muse.ai's router did not open a new thread; navigating",
			slog.String("event", "muse.router_missed"))

		if err := held(d.page.Navigate(ctx, Site+"/thread/new")); err != nil {
			return err
		}

		return d.untilReady(ctx, func(info pageInfo) bool { return info.Path == "/thread/new" && info.Empty })
	}

	return err
}

// openThread opens a thread to continue it.
func (d *driver) openThread(ctx context.Context, thread string) error {
	if err := held(d.page.Navigate(ctx, ThreadURL(thread))); err != nil {
		return err
	}

	return d.untilReady(ctx, func(info pageInfo) bool { return ThreadID(info.Path) == thread })
}

// untilReady waits for the page to be ready where there says.
func (d *driver) untilReady(ctx context.Context, there func(pageInfo) bool) error {
	end := d.now().Add(d.t.ready)

	for {
		var info pageInfo
		if err := d.eval(ctx, pageState, &info); err != nil {
			return err
		}

		switch {
		case info.Login:
			return ErrNeedsSignIn
		case info.Ready && there(info):
			return nil
		case d.now().After(end):
			return fmt.Errorf("%w: at %s, composer %t", ErrPageNotReady, info.Path, info.Composer)
		}

		if err := d.sleep(ctx, d.t.poll); err != nil {
			return err
		}
	}
}

type chat struct {
	Count       int    `json:"count"`
	Last        string `json:"last"`
	Busy        bool   `json:"busy"`
	Approval    string `json:"approval"`
	Error       string `json:"error"`
	Composer    string `json:"composer"`
	Stalled     bool   `json:"stalled"`
	Undelivered bool   `json:"undelivered"`
}

// state reads the conversation.
func (d *driver) state(ctx context.Context) (chat, error) {
	var c chat

	err := d.eval(ctx, chatState, &c)

	return c, err
}

// send puts the prompt in the composer and sends it (row 11). pressed says
// whether the prompt may have reached Muse: from the press of Send on.
func (d *driver) send(ctx context.Context, prompt string) (pressed bool, err error) {
	var rect *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}

	if err := d.eval(ctx, composerRect, &rect); err != nil {
		return false, err
	}

	if rect == nil {
		return false, fmt.Errorf("%w: no composer", ErrSendFailed)
	}

	if err := held(d.page.Click(ctx, rect.X, rect.Y)); err != nil {
		return false, err
	}

	var length int
	if err := d.eval(ctx, setComposer(prompt), &length); err != nil {
		return false, err
	}

	if length == 0 {
		// The setter did not take: the text is typed as an input method does (czg's fallback).
		if err := held(d.page.InsertText(ctx, prompt)); err != nil {
			return false, err
		}
	}

	start := d.now()

	// From the press on, the prompt may have reached Muse.
	if err := d.press(ctx); err != nil {
		return true, err
	}

	again := false

	for {
		c, err := d.state(ctx)
		if err != nil {
			return true, err
		}

		elapsed := d.now().Sub(start)

		switch {
		case strings.TrimSpace(c.Composer) == "" || c.Busy:
			return true, nil
		case elapsed >= d.t.sendFail:
			// Still in the composer: it never left.
			return false, fmt.Errorf("%w: it stayed in the composer", ErrSendFailed)
		case elapsed >= d.t.resend && !again && strings.TrimSpace(c.Composer) != "":
			// Still in the composer with no answer: Send once more (czg's engine.py:1350-1359).
			again = true

			d.logger.InfoContext(ctx, "the prompt stayed in muse.ai's composer; sending it again",
				slog.String("event", "muse.resend"))

			if err := d.press(ctx); err != nil {
				return true, err
			}
		}

		if err := d.sleep(ctx, d.t.poll); err != nil {
			return true, err
		}
	}
}

// press clicks Send, waiting up to sendConfirm for it to be there and
// enabled; Enter when it never is.
func (d *driver) press(ctx context.Context) error {
	end := d.now().Add(d.t.sendConfirm)

	for {
		var clicked string
		if err := d.eval(ctx, clickSend, &clicked); err != nil {
			return err
		}

		if clicked == "clicked" {
			return nil
		}

		if d.now().After(end) {
			return held(d.page.KeyEnter(ctx))
		}

		if err := d.sleep(ctx, d.t.poll/2); err != nil {
			return err
		}
	}
}

// wait waits for the answer after base to end (row 12): busy while the stop
// button shows, done when not busy and the new answer unchanged for stable.
func (d *driver) wait(ctx context.Context, base chat, deadline time.Time) (chat, error) {
	start := d.now()

	var (
		last       chat
		got        bool
		changed    = start
		stalled    time.Time
		undeliverd time.Time
	)

	for {
		c, err := d.state(ctx)
		if err != nil {
			return last, err
		}

		now := d.now()
		fresh := c.Count > base.Count || (c.Last != "" && c.Last != base.Last)

		switch {
		case c.Approval != "":
			return c, ErrAsksPermission
		case c.Error != "" && (fresh || !c.Busy):
			return c, fmt.Errorf("%w: %s", ErrAnswerError, c.Error)
		}

		if fresh && c.Last != "" {
			if !got || c.Last != last.Last {
				changed = now
			}

			got = true
			last = c

			if !c.Busy && now.Sub(changed) >= d.t.stable {
				return c, nil
			}
		}

		if !got {
			if c.Stalled && stalled.IsZero() {
				stalled = now
			}

			if !c.Stalled {
				stalled = time.Time{}
			}

			switch {
			case !stalled.IsZero() && now.Sub(start) >= d.t.stall:
				return c, fmt.Errorf("%w: muse.ai stayed connecting", ErrUnknown)
			case now.Sub(start) >= d.t.firstToken:
				return c, fmt.Errorf("%w: no answer began within %s", ErrUnknown, d.t.firstToken)
			}
		}

		if c.Undelivered {
			if undeliverd.IsZero() {
				undeliverd = now
			}

			if now.Sub(undeliverd) >= d.t.undelivered {
				return c, fmt.Errorf("%w: Muse said the delivery was not confirmed", ErrUnknown)
			}
		} else {
			undeliverd = time.Time{}
		}

		if now.After(deadline) {
			return last, fmt.Errorf("%w: no finished answer by the deadline", ErrUnknown)
		}

		if err := d.sleep(ctx, d.t.poll); err != nil {
			return last, fmt.Errorf("%w: %w", ErrUnknown, err)
		}
	}
}

// read reads the answer after the base messages and the thread's id (row 14).
func (d *driver) read(ctx context.Context, base int) (answer, error) {
	var got struct {
		Matched string   `json:"matched"`
		Text    string   `json:"text"`
		Links   []Link   `json:"links"`
		Media   []string `json:"media"`
	}

	if err := d.eval(ctx, readAnswer(base), &got); err != nil {
		return answer{}, err
	}

	d.logger.DebugContext(ctx, "muse.ai's answer was read", slog.String("event", "muse.read"),
		slog.String("matched", got.Matched))

	if got.Matched == "bubble" {
		d.logger.InfoContext(ctx, "muse.ai's answer was found by its bubble, not its message item",
			slog.String("event", "muse.read_fallback"))
	}

	thread, err := d.thread(ctx)

	return answer{Text: got.Text, Links: got.Links, Media: got.Media, Thread: thread}, err
}

// thread is the thread's id from the page's address, waited for threadID.
func (d *driver) thread(ctx context.Context) (string, error) {
	end := d.now().Add(d.t.threadID)

	for {
		var path string
		if err := d.eval(ctx, threadPath, &path); err != nil {
			return "", err
		}

		if id := ThreadID(path); id != "" {
			return id, nil
		}

		if d.now().After(end) {
			return "", nil
		}

		if err := d.sleep(ctx, d.t.poll); err != nil {
			return "", err
		}
	}
}

// quotaHint is the quota hint in a finished answer, or empty (row 9: only
// inside a finished reply, www's driver.py:437-451).
func quotaHint(text string) string {
	low := strings.ToLower(text)

	for _, hint := range quotaHints {
		if i := strings.Index(low, hint); i >= 0 {
			from, to := max(0, i-120), min(len(text), i+len(hint)+120)

			return strings.Join(strings.Fields(text[from:to]), " ")
		}
	}

	return ""
}

// ask asks one question: in thread when it is given, else in a new one.
// sent says whether the prompt may have reached Muse.
func (d *driver) ask(ctx context.Context, prompt, thread string, deadline time.Time) (a answer, sent bool, err error) {
	if thread != "" {
		err = d.openThread(ctx, thread)
	} else {
		err = d.newThread(ctx)
	}

	if err != nil {
		return answer{}, false, err
	}

	// Only what comes after this counts: a continued thread shows its earlier answers.
	base, err := d.state(ctx)
	if err != nil {
		return answer{}, false, err
	}

	if pressed, err := d.send(ctx, prompt); err != nil {
		return answer{}, pressed, err
	}

	done, err := d.wait(ctx, base, deadline)

	switch {
	case errors.Is(err, ErrAsksPermission):
		a.Approval = done.Approval
		a.Thread, _ = d.thread(ctx)

		return a, true, err
	case errors.Is(err, ErrAnswerError):
		a.Error = done.Error
		a.Thread, _ = d.thread(ctx)

		return a, true, err
	case err != nil:
		a.Thread, _ = d.thread(ctx)

		return a, true, err
	}

	if a, err = d.read(ctx, base.Count); err != nil {
		return a, true, err
	}

	if a.Text == "" {
		a.Text = done.Last
	}

	// The quota is Muse's final word, its last message, never words an answer of several messages quotes before it
	// (review of #1292: a quoted "usage limit" rested an account).
	if hint := quotaHint(done.Last); hint != "" {
		return a, true, fmt.Errorf("%w: …%s…", ErrQuota, hint)
	}

	return a, true, nil
}

// usage opens Settings → Usage and reads its text (row 9, www's dom.py
// QUOTA_SETTINGS_ENTRY, QUOTA_MENU_ITEM, QUOTA_DIALOG_TEXT). The page is left
// on a new thread.
func (d *driver) usage(ctx context.Context) (Usage, error) {
	if err := d.newThread(ctx); err != nil {
		return Usage{}, err
	}

	for _, script := range []string{settingsEntry, settingsItem} {
		var at *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}

		if err := d.eval(ctx, script, &at); err != nil {
			return Usage{}, err
		}

		if at == nil {
			return Usage{}, fmt.Errorf("%w: Muse's Settings were not found", ErrPageNotReady)
		}

		if err := held(d.page.Click(ctx, at.X, at.Y)); err != nil {
			return Usage{}, err
		}

		if err := d.sleep(ctx, d.t.poll); err != nil {
			return Usage{}, err
		}
	}

	end := d.now().Add(d.t.ready)

	for {
		var text string
		if err := d.eval(ctx, dialogText, &text); err != nil {
			return Usage{}, err
		}

		if u := ParseUsage(text); u.Found {
			return u, held(d.page.Navigate(ctx, Site+"/thread/new"))
		}

		if d.now().After(end) {
			return Usage{}, fmt.Errorf("%w: Muse's Usage was not shown", ErrPageNotReady)
		}

		if err := d.sleep(ctx, d.t.poll); err != nil {
			return Usage{}, err
		}
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}

	return s
}
