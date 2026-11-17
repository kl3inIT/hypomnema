package muse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// fakeAccounts are the gateway's Accounts.
type fakeAccounts struct {
	mu   sync.Mutex
	list []orchestration.Account
	next int
}

func (f *fakeAccounts) ListAccounts(context.Context) ([]orchestration.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]orchestration.Account{{ID: "acc_claude", Runtime: "claude", Position: 1}}, f.list...), nil
}

func (f *fakeAccounts) AddTokenAccount(_ context.Context, runtime, label string, ref func(string) string,
) (orchestration.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.next++
	a := orchestration.Account{ID: fmt.Sprintf("acc_%d", f.next), Runtime: runtime, Label: label,
		Status: orchestration.AccountActive, Position: len(f.list) + 1}
	a.SecretRef = ref(a.ID)
	f.list = append(f.list, a)

	return a, nil
}

func (f *fakeAccounts) RemoveAccountLogin(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.list = slices.DeleteFunc(f.list, func(a orchestration.Account) bool { return a.ID == id })

	return nil
}

func (f *fakeAccounts) MoveAccount(context.Context, string, bool) error { return nil }

func (f *fakeAccounts) set(id string, status orchestration.AccountStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := range f.list {
		if f.list[i].ID == id {
			f.list[i].Status = status
		}
	}
}

func (f *fakeAccounts) DisableAccount(_ context.Context, id string) error {
	f.set(id, orchestration.AccountDisabled)

	return nil
}

func (f *fakeAccounts) EnableAccount(_ context.Context, id string) error {
	f.set(id, orchestration.AccountActive)

	return nil
}

// fakeVault keeps generic secrets by name with their versions.
type fakeVault struct {
	mu      sync.Mutex
	values  map[string]string
	version map[string]int
	// beforeRotateAt runs before a version-checked save, as another writer would.
	beforeRotateAt func(name string)
	rotatedAt      int
}

func newFakeVault() *fakeVault {
	return &fakeVault{values: map[string]string{}, version: map[string]int{}}
}

func (f *fakeVault) Create(_ context.Context, n vault.NewSecret) (vault.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.values[n.Name], f.version[n.Name] = n.Payload[vault.KeyValue], 1

	return vault.Secret{Name: n.Name, Version: 1}, nil
}

func (f *fakeVault) Open(_ context.Context, name string) (vault.Opened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v, ok := f.values[name]
	if !ok {
		return vault.Opened{}, vault.ErrNotFound
	}

	return vault.Opened{Payload: vault.Payload{vault.KeyValue: v}, Version: f.version[name]}, nil
}

func (f *fakeVault) Rotate(_ context.Context, name string, p vault.Payload) (vault.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.values[name] = p[vault.KeyValue]
	f.version[name]++

	return vault.Secret{Name: name, Version: f.version[name]}, nil
}

func (f *fakeVault) RotateAt(ctx context.Context, name string, p vault.Payload, version int) (vault.Secret, error) {
	if f.beforeRotateAt != nil {
		f.beforeRotateAt(name)
	}

	f.mu.Lock()
	f.rotatedAt++

	if f.version[name] != version {
		f.mu.Unlock()

		return vault.Secret{}, vault.ErrConflict
	}

	f.mu.Unlock()

	return f.Rotate(ctx, name, p)
}

func (f *fakeVault) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.values, name)

	return nil
}

func (f *fakeVault) jar(name string) []browser.Cookie {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []browser.Cookie

	_ = json.Unmarshal([]byte(f.values[name]), &out)

	return out
}

// fakeBrowser gives each account a scripted muse.ai.
type fakeBrowser struct {
	mu       sync.Mutex
	sites    map[string]*museSite
	held     bool
	made     map[string][]browser.Cookie
	open     atomic.Int32
	most     atomic.Int32
	disposed []string
	// renewed is the session cookie a page gives back.
	renewed string
	profile []browser.Cookie
	cleared []browser.Cookie
	// gate, when set, is waited on by every chat read after the send.
	gate chan struct{}
}

func newFakeBrowser() *fakeBrowser {
	return &fakeBrowser{sites: map[string]*museSite{}, made: map[string][]browser.Cookie{}}
}

func (f *fakeBrowser) site(key string) *museSite {
	f.mu.Lock()
	defer f.mu.Unlock()

	m, ok := f.sites[key]
	if !ok {
		m = signedInSite()
		f.sites[key] = m
	}

	return m
}

func signedInSite() *museSite {
	m := site()
	m.auth = map[string]any{"status": 200, "ok": true, "viewer": "v"}
	m.session = map[string]any{"status": 200, "session": "assigned", "vm": "RUNNING"}
	m.usageText = "Usage\nFree plan\nWeekly limit resets on Sep 30\n10% used"

	return m
}

type accountPage struct {
	*navigating
	b *fakeBrowser
}

func (p *accountPage) Cookies(context.Context) ([]browser.Cookie, error) {
	p.b.mu.Lock()
	defer p.b.mu.Unlock()

	value := p.b.renewed
	if value == "" {
		value = "s1"
	}

	return []browser.Cookie{
		{Name: "hatch_sess", Value: value, Domain: ".muse.ai"},
		{Name: "hatch_native_auth_device", Value: "d", Domain: ".muse.ai"},
		{Name: "c_user", Value: "x", Domain: ".facebook.com"},
	}, nil
}

func (f *fakeBrowser) Page(_ context.Context, key string, cookies []browser.Cookie) (Page, error) {
	f.mu.Lock()
	held := f.held
	f.made[key] = cookies
	gate := f.gate
	f.mu.Unlock()

	if held {
		return nil, browser.ErrHeld
	}

	m := f.site(key)

	now := f.open.Add(1)
	for most := f.most.Load(); now > most && !f.most.CompareAndSwap(most, now); most = f.most.Load() {
	}

	page := &fakePage{answer: func(script string) (any, error) {
		f.mu.Lock()
		held := f.held
		f.mu.Unlock()

		if held {
			return nil, browser.ErrHeld
		}

		m.mu.Lock()
		sent := m.sent
		m.mu.Unlock()

		if gate != nil && sent && script == chatState {
			<-gate
		}

		return m.eval(script)
	}}

	return &countedPage{accountPage: &accountPage{navigating: &navigating{fakePage: page, site: m}, b: f}}, nil
}

// countedPage counts the pages in use: one ends with its last read of cookies.
type countedPage struct{ *accountPage }

func (p *countedPage) Cookies(ctx context.Context) ([]browser.Cookie, error) {
	defer p.b.open.Add(-1)

	return p.accountPage.Cookies(ctx)
}

func (f *fakeBrowser) DisposeContext(_ context.Context, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.disposed = append(f.disposed, key)
}

func (f *fakeBrowser) OpenOwnerTab(context.Context, string) error { return nil }

func (f *fakeBrowser) ProfileCookies(context.Context, string) ([]browser.Cookie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.profile, nil
}

func (f *fakeBrowser) ClearProfileCookies(_ context.Context, cookies []browser.Cookie) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cleared = append(f.cleared, cookies...)
	f.profile = nil

	return nil
}

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *stepClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

type pool struct {
	*Service
	accounts *fakeAccounts
	vault    *fakeVault
	browser  *fakeBrowser
	clock    *stepClock
}

func secretName(id string) string { return "muse-" + id }

// newPool is a pool with accounts labeled as given, in that order, each
// signed in.
func newPool(t *testing.T, labels ...string) *pool {
	t.Helper()

	p := &pool{accounts: &fakeAccounts{}, vault: newFakeVault(), browser: newFakeBrowser(),
		clock: &stepClock{now: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}}

	st := openTestStore(t)
	p.Service = newService(Config{
		Accounts: p.accounts, Secrets: p.vault, Browser: p.browser, SecretName: secretName, Clock: p.clock,
		Check: func(_ context.Context, cookies []browser.Cookie) (string, error) {
			return "viewer-" + cookies[0].Value, nil
		},
	}, st, &media{dir: t.TempDir(), http: http.DefaultClient, store: st})
	p.t = fastTimings

	for _, label := range labels {
		p.browser.profile = signedInJar("s0")

		in, err := p.SignInCheck(t.Context(), "", label)
		require.NoError(t, err)
		require.True(t, in.Done)
	}

	return p
}

func signedInJar(value string) []browser.Cookie {
	return []browser.Cookie{
		{Name: "hatch_sess", Value: value, Domain: ".muse.ai"},
		{Name: "hatch_native_auth_device", Value: "d", Domain: ".muse.ai"},
	}
}

func (p *pool) ask(t *testing.T, thread string) Result {
	t.Helper()

	res, err := p.Ask(t.Context(), Question{Prompt: "Who makes rice cookers?", Thread: thread, Agent: "worker"})
	require.NoError(t, err)

	return res
}

// A new thread takes the first account in order; the thread stays with it,
// even when an earlier account is back in use; its answer is kept with the
// cookies Muse renewed.
func TestANewThreadTakesTheFirstAccountAndKeepsIt(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first", "second")
	require.NoError(t, p.SetOn(t.Context(), "acc_1", false))

	res := p.ask(t, "")
	assert.Equal(t, OutcomeAnswered, res.Outcome)
	assert.Equal(t, "acc_2", res.Account)
	assert.Equal(t, "The answer [1]", res.Answer)
	assert.False(t, res.Unverified)
	assert.Equal(t, aThread, res.Thread)
	assert.Equal(t, "https://muse.ai/thread/"+aThread, res.ThreadURL)
	require.Len(t, res.Files, 1, "the answer's media are kept, or said why not")

	require.NoError(t, p.SetOn(t.Context(), "acc_1", true))

	p.browser.site("acc_2").sent = false
	p.browser.site("acc_2").shown = 0
	again := p.ask(t, aThread)
	assert.Equal(t, "acc_2", again.Account, "a thread stays with its account")

	messages, err := p.Messages(t.Context(), aThread, "", 0)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "worker", messages[0].Agent)
	assert.Equal(t, OutcomeAnswered, messages[0].Outcome)

	chats, err := p.Chats(t.Context(), "rice", 0)
	require.NoError(t, err)
	assert.Len(t, chats, 1)
}

// An answer with no link is marked unverified.
func TestAnAnswerWithoutLinksIsUnverified(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first")
	p.browser.site("acc_1").links = nil

	assert.True(t, p.ask(t, "").Unverified)
}

// A thread whose account rests, is off or was removed is not moved: the
// call says so.
func TestAThreadIsNotMovedOffItsAccount(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first", "second")
	p.ask(t, "")

	require.NoError(t, p.store.rest(t.Context(), "acc_1", true, "out of quota"))
	require.NoError(t, p.store.setUsage(t.Context(), "acc_1", Usage{UsedPercent: 100, Reset: "Sep 30", Found: true},
		p.clock.Now()))

	_, err := p.Ask(t.Context(), Question{Prompt: "more", Thread: aThread})
	require.ErrorIs(t, err, ErrNoAccount)
	assert.Contains(t, err.Error(), "resting (out of quota, 100% used · resets Sep 30); the thread stays with it")

	require.NoError(t, p.Remove(t.Context(), "acc_1"))

	_, err = p.Ask(t.Context(), Question{Prompt: "more", Thread: aThread})
	require.ErrorIs(t, err, ErrNoAccount)
	assert.Contains(t, err.Error(), "removed")
	assert.Empty(t, p.vault.jar(secretName("acc_1")), "its cookies went with it")

	_, err = p.Ask(t.Context(), Question{Prompt: "more", Thread: "0b3c1f7e-2a4d-4c5e-9f60-000000000000"})
	require.ErrorIs(t, err, ErrInvalid)
}

// A quota hint in the reply rests the account; the next new thread takes the
// next account; after six hours a Usage read that shows room takes it back.
func TestQuotaRestsAnAccountUntilUsageShowsRoom(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first", "second")
	first := p.browser.site("acc_1")
	first.answerText = "You have reached your weekly limit."
	first.after = answered(first.answerText)
	first.usageText = "Usage\nWeekly limit resets on Sep 30\n100% used"

	res := p.ask(t, "")
	assert.Equal(t, OutcomeError, res.Outcome)
	assert.Contains(t, res.Note, "out of quota")

	views, err := p.Accounts(t.Context())
	require.NoError(t, err)
	assert.True(t, views[0].Resting)
	assert.Equal(t, "100% used · resets Sep 30", views[0].Usage)

	assert.Equal(t, "acc_2", p.ask(t, "").Account)

	// Muse's week turns; the first account's Usage is read again once six hours passed.
	first.usageText = "Usage\nWeekly limit resets on Oct 7\n0% used"
	first.answerText = "The answer [1]"
	first.after = answered(first.answerText)
	first.sent, first.shown = false, 0
	p.clock.add(time.Hour)
	assert.Equal(t, "acc_2", p.ask(t, "").Account, "not read again within six hours")

	p.clock.add(6 * time.Hour)
	assert.Equal(t, "acc_1", p.ask(t, "").Account)
}

// A 403 marks the account refused and the question goes to the next; it is
// tried again later and works. A workspace not assigned is its own state.
func TestRefusedThenSignedInAndWorkspaceNotReady(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first", "second")
	first := p.browser.site("acc_1")
	first.auth = map[string]any{"status": 403}

	assert.Equal(t, "acc_2", p.ask(t, "").Account)

	views, err := p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, StateRefused, views[0].State)
	assert.Contains(t, views[0].Cause, "403")

	first.auth = map[string]any{"status": 200, "ok": true, "viewer": "v"}
	assert.Equal(t, "acc_1", p.ask(t, "").Account, "a refused account is tried again")

	views, err = p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, StateSignedIn, views[0].State)

	first.session = map[string]any{"status": 200, "session": "pending"}
	assert.Equal(t, "acc_2", p.ask(t, "").Account)

	views, err = p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, StateWorkspace, views[0].State)

	first.auth = map[string]any{"status": 401}
	p.browser.site("acc_2").auth = map[string]any{"status": 401}

	_, err = p.Ask(t.Context(), Question{Prompt: "x"})
	require.ErrorIs(t, err, ErrNoAccount)

	_, err = p.Ask(t.Context(), Question{Prompt: "x"})
	require.ErrorIs(t, err, ErrNoAccount)
	assert.Contains(t, err.Error(), "needs to be signed in again")
}

// The owner holding the browser interrupts the question: nothing it read is
// kept, and nothing goes to the vault.
func TestTheOwnersHoldInterruptsAndNothingIsSaved(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first")
	p.browser.held = true

	res := p.ask(t, "")
	assert.Equal(t, OutcomeInterrupted, res.Outcome)
	assert.Contains(t, res.Note, "Interrupted by the owner")
	assert.Zero(t, p.vault.rotatedAt, "no cookie is saved")

	// Taken while waiting for the answer.
	p.browser.held = false
	m := p.browser.site("acc_1")
	m.after = []chat{{Count: 1, Last: "partial", Busy: true}}

	go func() {
		for {
			m.mu.Lock()
			shown := m.shown
			m.mu.Unlock()

			if shown > 2 {
				p.browser.mu.Lock()
				p.browser.held = true
				p.browser.mu.Unlock()

				return
			}

			time.Sleep(time.Millisecond)
		}
	}()

	res = p.ask(t, "")
	assert.Equal(t, OutcomeInterrupted, res.Outcome)
	assert.Empty(t, res.Answer)
	assert.Zero(t, p.vault.rotatedAt)

	kept, err := p.store.messages(t.Context(), "", "", 0)
	require.NoError(t, err)
	require.Len(t, kept, 2)
	assert.Empty(t, kept[1].Answer, "what was read is not kept")
}

// Two questions at once, on one account or on two, run one after the other.
func TestQuestionsRunOneAtATime(t *testing.T) {
	t.Parallel()

	for _, accounts := range [][]string{{"only"}, {"first", "second"}} {
		p := newPool(t, accounts...)

		var wg sync.WaitGroup

		for range 4 {
			wg.Go(func() {
				_, err := p.Ask(t.Context(), Question{Prompt: "hello"})
				assert.NoError(t, err)
			})
		}

		wg.Wait()
		assert.Equal(t, int32(1), p.browser.most.Load(), "one Muse tab at a time")
	}
}

// A Usage read waits for the question in its turn.
func TestAUsageReadWaitsForTheQuestion(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first", "second")
	require.NoError(t, p.store.rest(t.Context(), "acc_2", true, "quota"))

	gate := make(chan struct{})
	p.browser.gate = gate

	done := make(chan struct{})

	go func() {
		defer close(done)

		_, err := p.Ask(t.Context(), Question{Prompt: "hello"})
		assert.NoError(t, err)
	}()

	first := p.browser.site("acc_1")

	for sent := false; !sent; {
		time.Sleep(time.Millisecond)
		first.mu.Lock()
		sent = first.sent
		first.mu.Unlock()
	}

	p.clock.add(7 * time.Hour)

	read := make(chan struct{})

	go func() {
		defer close(read)

		release, err := p.take(t.Context())
		assert.NoError(t, err)
		defer release()

		p.recheckRest(t.Context(), orchestration.Account{ID: "acc_2"}, accountState{Resting: true})
	}()

	select {
	case <-read:
		t.Fatal("the Usage read ran during the question")
	case <-time.After(20 * time.Millisecond):
	}

	close(gate)
	<-done
	<-read
}

// A cookie jar renewed by a question does not overwrite a newer sign-in; a
// failed question still saves the cookies it was given.
func TestARenewedJarNeverOverwritesANewerSignIn(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first")
	p.browser.renewed = "renewed"
	name := secretName("acc_1")
	// The Usage was just read: no read after the answer saves the jar a second time.
	require.NoError(t, p.store.setUsage(t.Context(), "acc_1", Usage{}, p.clock.Now()))

	p.vault.beforeRotateAt = func(name string) {
		p.vault.beforeRotateAt = nil
		_, _ = p.vault.Rotate(t.Context(), name, vault.Payload{vault.KeyValue: `[{"name":"hatch_sess","value":"newer"}]`})
	}

	p.ask(t, "")
	assert.Equal(t, "newer", p.vault.jar(name)[0].Value, "the newer sign-in stays")

	// A send that fails still saves what Muse renewed.
	m := p.browser.site("acc_1")
	m.sent, m.shown, m.composerSticks = false, 0, true

	res := p.ask(t, "")
	assert.Equal(t, OutcomeError, res.Outcome)
	assert.Equal(t, "renewed", p.vault.jar(name)[0].Value)
}

// Chrome going away after the send: sent, result unknown, and the context
// is dropped for the next question.
func TestAGoneTabIsAnUnknownResult(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first")
	m := p.browser.site("acc_1")
	m.after = []chat{{Count: 1, Last: "partial", Busy: true}}

	b := p.browser

	p.cfg.Browser = goneAfterSend{fakeBrowser: b}

	res := p.ask(t, "")
	assert.Equal(t, OutcomeUnknown, res.Outcome)
	assert.Contains(t, res.Note, "Sent, result unknown")
	assert.Contains(t, b.disposed, "acc_1")
}

type goneAfterSend struct{ *fakeBrowser }

func (g goneAfterSend) Page(ctx context.Context, key string, cookies []browser.Cookie) (Page, error) {
	page, err := g.fakeBrowser.Page(ctx, key, cookies)
	if err != nil {
		return nil, err
	}

	m := g.site(key)

	return &goneEval{Page: page, m: m}, nil
}

type goneEval struct {
	Page
	m *museSite
}

func (g *goneEval) Evaluate(ctx context.Context, script string, out any) error {
	g.m.mu.Lock()
	sent := g.m.sent
	g.m.mu.Unlock()

	if sent && script == chatState {
		return browser.ErrPageGone
	}

	return g.Page.Evaluate(ctx, script, out)
}

// The sign-in is captured while the owner holds the browser: it waits for
// muse.ai's session, adds the account with its jar, and clears muse.ai's
// cookies from the shared profile; signing in again replaces the jar.
func TestASignInIsCapturedAndClearedFromTheProfile(t *testing.T) {
	t.Parallel()

	p := newPool(t)
	p.browser.held = true

	in, err := p.SignInCheck(t.Context(), "", "research")
	require.NoError(t, err)
	assert.False(t, in.Done)
	assert.Contains(t, in.Waiting, "waiting")

	p.browser.profile = append(signedInJar("s1"), browser.Cookie{Name: "c_user", Value: "1", Domain: ".facebook.com"})

	in, err = p.SignInCheck(t.Context(), "", "research")
	require.NoError(t, err)
	require.True(t, in.Done)
	assert.Equal(t, "research", in.Account.Label)
	assert.Equal(t, StateSignedIn, in.Account.State)
	assert.Equal(t, "unknown", in.Account.Usage)
	assert.Len(t, p.vault.jar(secretName("acc_1")), 2, "only muse.ai's cookies are kept")
	assert.Len(t, p.browser.cleared, 2, "only muse.ai's cookies are cleared")

	p.browser.profile = signedInJar("s2")

	in, err = p.SignInCheck(t.Context(), "acc_1", "")
	require.NoError(t, err)
	require.True(t, in.Done)
	assert.Equal(t, "s2", p.vault.jar(secretName("acc_1"))[0].Value)
	assert.Contains(t, p.browser.disposed, "acc_1", "the old context goes")

	accounts, err := p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Len(t, accounts, 1, "signing in again adds no account; other runtimes are not Muse's")
}

// muse.ai's sign-in check: ok with a viewer, 401, 403.
func TestTheSignInCheck(t *testing.T) {
	t.Parallel()

	answer := ""
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/auth/check", r.URL.Path)
		assert.Contains(t, r.Header.Get("Cookie"), "hatch_sess=s")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()

	answer = `{"ok":true,"viewer_id":12345}`
	viewer, err := checkSignIn(t.Context(), srv.Client(), srv.URL, signedInJar("s"))
	require.NoError(t, err)
	assert.Equal(t, "12345", viewer)

	answer = `{"ok":false}`
	_, err = checkSignIn(t.Context(), srv.Client(), srv.URL, signedInJar("s"))
	require.ErrorIs(t, err, ErrNeedsSignIn)

	status = http.StatusForbidden
	_, err = checkSignIn(t.Context(), srv.Client(), srv.URL, signedInJar("s"))
	require.ErrorIs(t, err, ErrRefused)
}

// What an agent asks is checked before any account is taken.
func TestABadQuestionIsRefused(t *testing.T) {
	t.Parallel()

	p := newPool(t)

	for _, q := range []Question{{}, {Prompt: "x", Deadline: time.Hour}, {Prompt: "x", Thread: "not-a-thread"}} {
		_, err := p.Ask(t.Context(), q)
		require.ErrorIs(t, err, ErrInvalid)
	}

	_, err := p.Ask(t.Context(), Question{Prompt: "x"})
	require.ErrorIs(t, err, ErrNoAccount)
	assert.Contains(t, err.Error(), "no Muse account was added")
	assert.NotErrorIs(t, err, ErrInvalid)
}
