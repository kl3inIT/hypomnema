package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	// owner is what the sign-in check fetched in the owner's tab answers; nil is ok, with a viewer made of
	// the profile's session cookie. ownerScripts are the scripts run there.
	owner        map[string]any
	ownerErr     error
	ownerScripts []string
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
	b       *fakeBrowser
	cookies []browser.Cookie
}

func (p *accountPage) Cookies(context.Context) ([]browser.Cookie, error) {
	p.b.mu.Lock()
	defer p.b.mu.Unlock()

	value := p.b.renewed
	if value == "" {
		return p.cookies, nil
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

	return &countedPage{accountPage: &accountPage{
		navigating: &navigating{fakePage: page, site: m}, b: f, cookies: cookies,
	}}, nil
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

func (f *fakeBrowser) EvaluateOwnerTab(_ context.Context, expression string, result any) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.held {
		return browser.ErrHeld
	}

	f.ownerScripts = append(f.ownerScripts, expression)

	if f.ownerErr != nil {
		return f.ownerErr
	}

	answer := f.owner
	if answer == nil {
		viewer := ""
		if len(f.profile) > 0 {
			viewer = "viewer-" + f.profile[0].Value
		}

		answer = map[string]any{"status": 200, "ok": true, "viewer": viewer}
	}

	raw, err := json.Marshal(answer)
	if err != nil {
		return err
	}

	return json.Unmarshal(raw, result)
}

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
		"", p.clock.Now()))

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

func TestUsageReadWarnsWhenTheReasonChangesAndClearsOnSuccess(t *testing.T) {
	t.Parallel()

	p := newPool(t, "research")
	require.NoError(t, p.store.setUsage(t.Context(), "acc_1",
		Usage{UsedPercent: 10, Reset: "Sep 30", Found: true}, "", p.clock.Now()))
	var logs bytes.Buffer
	p.cfg.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m := p.browser.site("acc_1")

	read := func(reason, wantUsage string, warnings int) {
		t.Helper()

		p.clock.add(usageEvery)
		require.Equal(t, OutcomeAnswered, p.ask(t, "").Outcome)
		accounts, err := p.Accounts(t.Context())
		require.NoError(t, err)
		assert.Equal(t, wantUsage, accounts[0].Usage)
		raw, err := json.Marshal(accounts[0])
		require.NoError(t, err)
		var view struct {
			UsageError string `json:"usage_error"`
		}
		require.NoError(t, json.Unmarshal(raw, &view))
		assert.Equal(t, reason, view.UsageError)

		var usageWarnings []string
		for line := range bytes.Lines(logs.Bytes()) {
			var entry struct {
				Event   string `json:"event"`
				Level   string `json:"level"`
				Account string `json:"account"`
				Error   string `json:"err"`
			}
			require.NoError(t, json.Unmarshal(line, &entry))
			if entry.Event == "muse.usage_unread" {
				assert.Equal(t, "WARN", entry.Level)
				assert.Equal(t, "acc_1", entry.Account)
				usageWarnings = append(usageWarnings, entry.Error)
			}
		}

		assert.Len(t, usageWarnings, warnings)
		if reason != "" {
			assert.Contains(t, usageWarnings, reason)
		}
	}

	const missingSettings = "muse.ai's page did not become ready: Muse's Settings were not found"
	const missingUsage = "muse.ai's page did not become ready: Muse's Usage was not shown"
	m.settingsMissing = true
	read(missingSettings, "10% used · resets Sep 30", 1)
	read(missingSettings, "10% used · resets Sep 30", 1)
	m.settingsMissing, m.usageText = false, ""
	read(missingUsage, "10% used · resets Sep 30", 2)
	m.usageText = "Usage\nWeekly limit resets on Sep 30\n15% used"
	read("", "15% used · resets Sep 30", 2)
	m.settingsMissing = true
	read(missingSettings, "15% used · resets Sep 30", 3)
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

// An account whose machine is not running is woken and asked, whether or not
// the machine is then seen running (issue #810); the account stays signed in,
// with what was seen as its cause.
func TestAnAccountWhoseMachineIsNotRunningIsAsked(t *testing.T) {
	t.Parallel()

	p := newPool(t, "first")
	first := p.browser.site("acc_1")
	first.session = map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "UNKNOWN"}
	first.awake = map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "RUNNING"}

	res := p.ask(t, "")
	assert.Equal(t, OutcomeAnswered, res.Outcome)
	assert.Equal(t, "acc_1", res.Account)
	assert.Len(t, first.woken, 1)

	views, err := p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, StateSignedIn, views[0].State)
	assert.Empty(t, views[0].Cause)

	// Woken, but never seen running: still asked.
	first.woken, first.sent, first.shown = nil, false, 0
	first.awake = map[string]any{"status": 200, "session": "assigned", "vmId": "vm1", "vm": "STARTING"}

	res = p.ask(t, "")
	assert.Equal(t, OutcomeAnswered, res.Outcome)
	assert.Len(t, first.woken, 1)

	views, err = p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, StateSignedIn, views[0].State)
	assert.Equal(t, "its machine was woken and is starting, not seen running", views[0].Cause)
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
	require.NoError(t, p.store.setUsage(t.Context(), "acc_1", Usage{}, "", p.clock.Now()))

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

// A sign-in is captured once the check fetched in the owner's tab answers
// ok: while the owner holds the browser it is not run, he is asked to give
// control back and nothing is saved; then the account is added with muse.ai's
// cookies only, which leave the shared profile; signing in again replaces the
// jar.
func TestASignInIsCapturedAndClearedFromTheProfile(t *testing.T) {
	t.Parallel()

	p := newPool(t)

	in, err := p.SignInCheck(t.Context(), "", "research")
	require.NoError(t, err)
	assert.False(t, in.Done)
	assert.Contains(t, in.Waiting, "waiting")
	assert.Empty(t, p.browser.ownerScripts, "no session in the profile, nothing asked")

	p.browser.profile = append(signedInJar("s1"), browser.Cookie{Name: "c_user", Value: "1", Domain: ".facebook.com"})
	p.browser.held = true

	in, err = p.SignInCheck(t.Context(), "", "research")
	require.NoError(t, err)
	assert.False(t, in.Done)
	assert.Equal(t, "Signed in? Give control back to finish adding the account.", in.Waiting)
	assert.Empty(t, p.vault.jar(secretName("acc_1")), "nothing is saved while the owner holds the browser")
	assert.Empty(t, p.browser.cleared)

	accounts, err := p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Empty(t, accounts)

	p.browser.held = false

	in, err = p.SignInCheck(t.Context(), "", "research")
	require.NoError(t, err)
	require.True(t, in.Done)
	assert.Equal(t, "research", in.Account.Label)
	assert.Equal(t, StateSignedIn, in.Account.State)
	assert.Equal(t, "10% used · resets Sep 30", in.Account.Usage)
	assert.Equal(t, 1, p.browser.site("acc_1").usageReads)
	assert.Len(t, p.vault.jar(secretName("acc_1")), 2, "only muse.ai's cookies are kept")
	assert.Len(t, p.browser.cleared, 2, "only muse.ai's cookies are cleared")
	assert.Equal(t, []string{ownerAuthCheck}, p.browser.ownerScripts, "the check is fetched in the owner's tab")

	p.browser.profile = signedInJar("s2")

	in, err = p.SignInCheck(t.Context(), "acc_1", "")
	require.NoError(t, err)
	require.True(t, in.Done)
	assert.Equal(t, 2, p.browser.site("acc_1").usageReads, "signing in again reads once")
	assert.Equal(t, "s2", p.vault.jar(secretName("acc_1"))[0].Value)
	assert.Contains(t, p.browser.disposed, "acc_1", "the old context goes")

	accounts, err = p.Accounts(t.Context())
	require.NoError(t, err)
	assert.Len(t, accounts, 1, "signing in again adds no account; other runtimes are not Muse's")
}

// The check fetched in the owner's tab: ok with a viewer adds the account;
// 401, ok false or no viewer wait for the sign-in; 403 says muse.ai refused;
// a tab not on muse.ai or not open waits; anything else is an error. Only ok
// saves anything.
func TestTheSignInCheckInTheOwnersTab(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		answer  map[string]any
		err     error
		waiting string
		failed  bool
	}{
		{"401", map[string]any{"status": 401, "ok": false}, nil, "finish signing in", false},
		{"ok false", map[string]any{"status": 200, "ok": false}, nil, "finish signing in", false},
		{"no viewer", map[string]any{"status": 200, "ok": true, "viewer": ""}, nil, "finish signing in", false},
		{"403", map[string]any{"status": 403, "ok": false}, nil, "muse.ai refused this session", false},
		{"not on muse.ai", map[string]any{"status": -1}, nil, "waiting for its session", false},
		{"no owner's tab", nil, browser.ErrNoSuchTab, "waiting for its session", false},
		{"fetch failed", map[string]any{"status": 0, "error": "TypeError: Failed to fetch"}, nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := newPool(t)
			p.browser.profile = signedInJar("s1")
			p.browser.owner, p.browser.ownerErr = tc.answer, tc.err

			in, err := p.SignInCheck(t.Context(), "", "research")
			if tc.failed {
				require.ErrorIs(t, err, ErrUnavailable)
			} else {
				require.NoError(t, err)
				assert.False(t, in.Done)
				assert.Contains(t, in.Waiting, tc.waiting)
			}

			assert.Empty(t, p.vault.jar(secretName("acc_1")), "nothing is saved")
			assert.Empty(t, p.browser.cleared, "the profile keeps its cookies")
		})
	}

	p := newPool(t)
	p.browser.profile = signedInJar("s1")
	p.browser.owner = map[string]any{"status": 200, "ok": true, "viewer": "12345"}

	in, err := p.SignInCheck(t.Context(), "", "")
	require.NoError(t, err)
	require.True(t, in.Done)
	assert.Equal(t, "Muse 12345", in.Account.Label, "an unnamed account is named after its viewer")
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
