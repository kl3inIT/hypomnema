package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Signed-in sources (issue #1291), with fakes only: X's web client served by a test server, LinkedIn's rendered
// pages by a fake tab, the vault and the shared browser's profile by maps. No real account, cookie or call.

// vaultFake keeps secrets by name.
type vaultFake struct {
	mu      sync.Mutex
	secrets map[string]string
}

func newVault() *vaultFake { return &vaultFake{secrets: map[string]string{}} }

func (v *vaultFake) Create(_ context.Context, n vault.NewSecret) (vault.Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.secrets[n.Name]; ok {
		return vault.Secret{}, vault.ErrConflict
	}

	v.secrets[n.Name] = n.Payload[vault.KeyValue]

	return vault.Secret{}, nil
}

func (v *vaultFake) Open(_ context.Context, name string) (vault.Opened, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	raw, ok := v.secrets[name]
	if !ok {
		return vault.Opened{}, vault.ErrNotFound
	}

	return vault.Opened{Payload: vault.Payload{vault.KeyValue: raw}, Version: 1}, nil
}

func (v *vaultFake) Rotate(_ context.Context, name string, p vault.Payload) (vault.Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.secrets[name]; !ok {
		return vault.Secret{}, vault.ErrNotFound
	}

	v.secrets[name] = p[vault.KeyValue]

	return vault.Secret{}, nil
}

func (v *vaultFake) Delete(_ context.Context, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.secrets[name]; !ok {
		return vault.ErrNotFound
	}

	delete(v.secrets, name)

	return nil
}

func (v *vaultFake) has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	_, ok := v.secrets[name]

	return ok
}

// browserFake is the shared browser: its profile's cookies, the owner's tab and the contexts made.
type browserFake struct {
	mu       sync.Mutex
	profile  []browser.Cookie
	opened   []string
	made     map[string][]browser.Cookie
	disposed []string
	pages    map[string]*pageFake
}

func newBrowser() *browserFake {
	return &browserFake{made: map[string][]browser.Cookie{}, pages: map[string]*pageFake{}}
}

func (b *browserFake) OpenOwnerTab(_ context.Context, address string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.opened = append(b.opened, address)

	return nil
}

func (b *browserFake) ProfileCookies(_ context.Context, domain string) ([]browser.Cookie, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []browser.Cookie

	for _, c := range b.profile {
		d := strings.TrimPrefix(c.Domain, ".")
		if d == domain || strings.HasSuffix(d, "."+domain) {
			out = append(out, c)
		}
	}

	return out, nil
}

func (b *browserFake) ClearProfileCookies(_ context.Context, cookies []browser.Cookie) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var kept []browser.Cookie

	for _, c := range b.profile {
		gone := false
		for _, g := range cookies {
			gone = gone || g.Name == c.Name && g.Domain == c.Domain
		}

		if !gone {
			kept = append(kept, c)
		}
	}

	b.profile = kept

	return nil
}

func (b *browserFake) Page(_ context.Context, key string, cookies []browser.Cookie) (listening.Page, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.made[key] = cookies

	p, ok := b.pages[key]
	if !ok {
		p = &pageFake{}
		b.pages[key] = p
	}

	return p, nil
}

func (b *browserFake) DisposeContext(_ context.Context, key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.disposed = append(b.disposed, key)
}

func (b *browserFake) profileNames() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := []string{}
	for _, c := range b.profile {
		out = append(out, c.Name)
	}

	return out
}

// pageFake is a tab whose snapshot answers what is set for the address it navigated to.
type pageFake struct {
	mu     sync.Mutex
	at     string
	visits []string
	// responses are what the requests the page sent since its last navigation were answered (browser.Response);
	// respond, when set, answers them instead, by the address and the scrolls; bodies are their bodies by request id.
	responses []browser.Response
	respond   func(address string, scrolls int) []browser.Response
	bodies    map[string]string
	// bodyFails is how many reads of each body fail first, as a body not loaded yet does.
	bodyFails map[string]int
	scrolls   int
	snapshot  func(address string, scrolls int) map[string]any
	// eval, when set, answers every script but a scroll, by the script and the address.
	eval   func(expression, address string) any
	hovers [][2]float64
	clicks [][2]float64
}

func (p *pageFake) Responses(context.Context) ([]browser.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.respond != nil {
		return p.respond(p.at, p.scrolls), nil
	}

	return p.responses, nil
}

func (p *pageFake) Body(_ context.Context, r browser.Response) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.bodyFails[r.RequestID] > 0 {
		p.bodyFails[r.RequestID]--

		return nil, fmt.Errorf("the body of %q has not loaded yet", r.RequestID)
	}

	body, ok := p.bodies[r.RequestID]
	if !ok {
		return nil, fmt.Errorf("no body for %q", r.RequestID)
	}

	return []byte(body), nil
}

func (p *pageFake) Hover(_ context.Context, x, y float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.hovers = append(p.hovers, [2]float64{x, y})

	return nil
}

func (p *pageFake) Click(_ context.Context, x, y float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.clicks = append(p.clicks, [2]float64{x, y})

	return nil
}

func (p *pageFake) Navigate(_ context.Context, address string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.at, p.scrolls = address, 0
	p.visits = append(p.visits, address)

	return nil
}

func (p *pageFake) Evaluate(_ context.Context, expression string, result any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if strings.HasPrefix(expression, "window.scrollBy") {
		p.scrolls++

		return nil
	}

	var answer any = map[string]any{"href": p.at, "status": 200, "ready": "complete", "posts": []any{}, "empty": false}

	switch {
	case p.eval != nil:
		answer = p.eval(expression, p.at)
	case p.snapshot != nil:
		answer = p.snapshot(p.at, p.scrolls)
	}

	if result == nil {
		return nil
	}

	raw, err := json.Marshal(answer)
	if err != nil {
		return err
	}

	return json.Unmarshal(raw, result)
}

// snowflake is the X id of a post made at t (x_api.py:138-151, the other way).
func snowflake(t time.Time) string {
	return fmt.Sprint((t.UnixMilli() - 1288834974657) << 22)
}

const testBearer = "AAAAAAAAAAAAAAAAAAAAAtestbearerTESTBEARERtestbearerTESTBEARER1234"

// xFake is X's web client: its home page linking a bundle, the bundle with the bearer and a query id, and
// SearchTimeline.
type xFake struct {
	t        *testing.T
	mu       sync.Mutex
	queryID  string
	searches int
	status   int
	tweets   []map[string]any
	cookies  []string
	asked    []string
	noCookie []string
	// bundleTo redirects the bundle's request there when set.
	bundleTo string
}

func (x *xFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x.mu.Lock()
		defer x.mu.Unlock()

		switch {
		case r.URL.Path == "/" || r.URL.Path == "/explore":
			x.noCookie = append(x.noCookie, r.Header.Get("Cookie"))
			_, _ = fmt.Fprint(w, `<script src="https://abs.twimg.com/responsive-web/client-web/main.abc123.js">`+
				`</script>`)
		case strings.HasPrefix(r.URL.Path, "/responsive-web/") && x.bundleTo != "":
			http.Redirect(w, r, x.bundleTo, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/responsive-web/"):
			x.noCookie = append(x.noCookie, r.Header.Get("Cookie"))
			_, _ = fmt.Fprintf(w, `var a="%s";e.exports={queryId:"%s",operationName:"SearchTimeline",`+
				`operationType:"query"}`, testBearer, x.queryID)
		case strings.HasSuffix(r.URL.Path, "/SearchTimeline"):
			x.searches++
			x.cookies = append(x.cookies, r.Header.Get("Cookie"))
			x.asked = append(x.asked, r.URL.Query().Get("variables"))

			id := strings.Split(r.URL.Path, "/")[4]
			if id != x.queryID {
				w.WriteHeader(http.StatusNotFound)

				return
			}

			assert.Equal(x.t, "Bearer "+testBearer, r.Header.Get("Authorization"))
			assert.Equal(x.t, "csrf-1", r.Header.Get("X-Csrf-Token"), "ct0 goes as the CSRF token")

			if x.status != 0 {
				w.Header().Set("X-Rate-Limit-Reset", fmt.Sprint(start.Add(15*time.Minute).Unix()))
				w.WriteHeader(x.status)

				return
			}

			entries := []any{}
			for _, tw := range x.tweets {
				entries = append(entries, map[string]any{"content": map[string]any{
					"itemContent": map[string]any{"tweet_results": map[string]any{"result": tw}},
				}})
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"search_by_raw_query": map[string]any{
				"search_timeline": map[string]any{"timeline": map[string]any{"instructions": []any{
					map[string]any{"entries": entries},
				}}},
			}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func tweet(id, user, text string, likes float64) map[string]any {
	return map[string]any{
		"__typename": "Tweet", "rest_id": id,
		"core":   map[string]any{"user_results": map[string]any{"result": map[string]any{"core": map[string]any{"screen_name": user}}}},
		"legacy": map[string]any{"full_text": text, "favorite_count": likes, "retweet_count": 2, "reply_count": 1},
	}
}

// signedRig is a Service with X and LinkedIn, X's web client a test server, its vault, browser and owner's channel
// fakes.
type signedRig struct {
	svc     *listening.Service
	x       *xFake
	vault   *vaultFake
	browser *browserFake
	told    *told
	clock   *clock
	web     listening.WebSearch
	webAsks *[]string
}

func newSignedRig(t *testing.T) *signedRig {
	t.Helper()

	return newSignedRigWith(t, nil)
}

// newSignedRigWith is newSignedRig whose client reaches other hosts' test servers too.
func newSignedRigWith(t *testing.T, hosts map[string]string) *signedRig {
	t.Helper()

	x := &xFake{t: t, queryID: "liveQueryId1"}
	srv := httptest.NewServer(x.handler())
	t.Cleanup(srv.Close)

	servers := map[string]string{"x.com": srv.URL, "abs.twimg.com": srv.URL}
	for h, u := range hosts {
		servers[h] = u
	}

	client := hostsTo(servers)
	rig := &signedRig{x: x, vault: newVault(), browser: newBrowser(), told: &told{}, clock: newClock()}

	asks := []string{}
	rig.webAsks = &asks
	rig.web = func(_ context.Context, query string, _ int) (websearch.Answer, error) {
		*rig.webAsks = append(*rig.webAsks, query)

		return websearch.Answer{Provider: "fake", Results: []websearch.Result{{
			URL: "https://x.com/someone/status/" + snowflake(start.Add(-48*time.Hour)), Title: "a post",
			Snippet: "VETC thu phí sai",
		}}}, nil
	}

	rig.svc = listening.New(listening.Config{
		Platforms: listening.SignedSources(listening.Endpoints{}, client), Now: rig.clock.Now, Sleep: noSleep,
		DB: migrated(t), Tell: rig.told.tell, Browser: rig.browser, Secrets: rig.vault,
	})

	return rig
}

func (r *signedRig) signInX(t *testing.T) {
	t.Helper()

	require.NoError(t, r.svc.Consent(t.Context(), listening.SourceX))
	require.NoError(t, r.svc.PasteCookies(t.Context(), listening.SourceX,
		"guest_id=g1; auth_token=token-1; ct0=csrf-1", "secondary"))
}

func (r *signedRig) search(t *testing.T, source string) listening.Answer {
	t.Helper()

	p := plan([]string{source}, "VETC")
	p.WindowDays = 30

	a, err := r.svc.Search(t.Context(), p, listening.Caller{Web: r.web})
	require.NoError(t, err)

	return a
}

func TestSignedSourceIsOffUntilItsConsentCardIsAgreedTo(t *testing.T) {
	rig := newSignedRig(t)

	on, err := rig.svc.On(t.Context())
	require.NoError(t, err)
	assert.Empty(t, on, "X and LinkedIn are off until the owner agrees to their consent cards")

	err = rig.svc.SetSource(t.Context(), listening.SourceX, true)
	require.ErrorIs(t, err, listening.ErrInvalid, "the switch alone does not turn a signed-in source on")

	err = rig.svc.SignInStart(t.Context(), listening.SourceX)
	require.ErrorIs(t, err, listening.ErrInvalid, "no sign-in before the consent")
	assert.Empty(t, rig.browser.opened)

	st := stateOf(t, rig.svc, listening.SourceX)
	require.NotNil(t, st.Signed)
	assert.Contains(t, st.Signed.Consent, "suspended")
	assert.True(t, st.Signed.ConsentedAt.IsZero())

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceX))

	on, err = rig.svc.On(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{listening.SourceX}, on, "consent is per source: LinkedIn stays off")

	require.NoError(t, rig.svc.SignInStart(t.Context(), listening.SourceX))
	assert.Equal(t, []string{"https://x.com/i/flow/login"}, rig.browser.opened, "the owner signs in in his own tab")
}

func TestXReadsAsTheAccountWithItsCookiesOnlyToXAndDatesFromIDs(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)

	inside := start.Add(-72 * time.Hour)
	rig.x.tweets = []map[string]any{
		tweet(snowflake(inside), "rider", "VETC charged me twice", 40),
		tweet(snowflake(start.AddDate(0, 0, -90)), "old", "VETC long ago", 3),
		tweet("not-a-number", "bad", "VETC with an id that dates nothing", 1),
	}

	a := rig.search(t, listening.SourceX)

	cov := coverageOf(t, a, listening.SourceX)
	assert.Equal(t, listening.BackendXGraphQL, cov.Streams[0].ServedBy)
	require.Len(t, a.Items, 1, "the post outside the window and the one whose id dates nothing are dropped")
	it := a.Items[0]
	assert.Equal(t, "https://x.com/rider/status/"+snowflake(inside), it.URL)
	assert.Equal(t, inside.Truncate(time.Millisecond), it.PublishedAt.UTC(), "the date is the snowflake id's")
	assert.InDelta(t, 40, it.Engagement["likes"], 0)
	assert.Equal(t, 1, cov.Dropped.MissingDate)

	rig.x.mu.Lock()
	defer rig.x.mu.Unlock()

	assert.Contains(t, rig.x.asked[len(rig.x.asked)-1], "VETC since:2026-10-28 until:2026-11-28",
		"the window goes as X's own operators, until exclusive")

	for _, c := range rig.x.cookies[1:] {
		assert.Equal(t, "guest_id=g1; auth_token=token-1; ct0=csrf-1", c)
	}

	for _, c := range rig.x.noCookie {
		assert.Empty(t, c, "X's pages and bundles are read without the account's cookies")
	}
}

func TestXReadsItsQueryIDsAgainWhenTheyRotate(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)
	rig.x.tweets = []map[string]any{tweet(snowflake(start.Add(-time.Hour)), "a", "VETC now", 1)}

	_ = rig.search(t, listening.SourceX)

	rig.x.mu.Lock()
	rig.x.queryID = "rotatedQueryId2"
	before := rig.x.searches
	rig.x.mu.Unlock()

	a := rig.search(t, listening.SourceX)
	require.Len(t, a.Items, 1, "a 404 on every known id reads X's bundles again, and the new id serves")

	rig.x.mu.Lock()
	defer rig.x.mu.Unlock()

	assert.Greater(t, rig.x.searches-before, 1)
}

func TestXSessionEndMarksTheAccountTellsOnceAndTheWebServesMeanwhile(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)
	rig.x.status = http.StatusUnauthorized

	a := rig.search(t, listening.SourceX)

	cov := coverageOf(t, a, listening.SourceX)
	assert.Equal(t, listening.BackendSiteSearch, cov.Streams[0].ServedBy, "the next free backend serves meanwhile")
	assert.Equal(t, string(listening.CodeNeedLogin), cov.Streams[0].Tried[0].Result)
	require.Len(t, a.Items, 1)
	assert.NotNil(t, a.Items[0].PublishedAt, "a post the web search found is dated by its id")

	st := stateOf(t, rig.svc, listening.SourceX)
	require.NotNil(t, st.Signed.Account)
	assert.Equal(t, listening.AccountNeedLogin, st.Signed.Account.State)
	require.Equal(t, 1, rig.told.Count())
	assert.Contains(t, rig.told.all[0].Fix, "sign it in again")

	rig.x.mu.Lock()
	searches := rig.x.searches
	rig.x.mu.Unlock()

	_ = rig.search(t, listening.SourceX)

	rig.x.mu.Lock()
	assert.Equal(t, searches, rig.x.searches, "an account that asked for a sign-in is not asked again")
	rig.x.mu.Unlock()
	assert.Equal(t, 1, rig.told.Count(), "the owner is told once")

	rig.x.status = 0
	rig.x.tweets = []map[string]any{tweet(snowflake(start.Add(-time.Hour)), "a", "VETC again", 1)}
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceX, "auth_token=token-2; ct0=csrf-1", ""))
	assert.Equal(t, listening.AccountSignedIn, stateOf(t, rig.svc, listening.SourceX).Signed.Account.State,
		"a sign-in again clears it")
}

func TestARefusedAccountIsNotAskedAgainUntilANewSignIn(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)
	rig.x.status = http.StatusForbidden

	_ = rig.search(t, listening.SourceX)
	assert.Equal(t, listening.AccountRefused, stateOf(t, rig.svc, listening.SourceX).Signed.Account.State)

	rig.x.mu.Lock()
	searches := rig.x.searches
	rig.x.mu.Unlock()

	// The owner's "Test again" ends the rests, not a refusal: a safety stop is not retried by waiting or testing.
	require.NoError(t, rig.svc.Test(t.Context(), listening.SourceX, rig.web, true))

	rig.x.mu.Lock()
	assert.Equal(t, searches, rig.x.searches)
	rig.x.mu.Unlock()
	assert.Equal(t, listening.AccountRefused, stateOf(t, rig.svc, listening.SourceX).Signed.Account.State)
}

func TestPastedCookiesNeedTheLoginCookiesAndAreVerifiedByARealSearch(t *testing.T) {
	rig := newSignedRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceX))

	err := rig.svc.PasteCookies(t.Context(), listening.SourceX, "auth_token=very-secret", "")
	require.ErrorIs(t, err, listening.ErrInvalid)
	assert.Contains(t, err.Error(), "ct0")
	assert.NotContains(t, err.Error(), "very-secret")
	assert.False(t, rig.vault.has("listening-x"))

	rig.x.status = http.StatusUnauthorized
	err = rig.svc.PasteCookies(t.Context(), listening.SourceX, "auth_token=expired; ct0=csrf-1", "")
	require.ErrorIs(t, err, listening.ErrInvalid, "cookies that are there but signed out are not kept")
	assert.Contains(t, err.Error(), "does not take the session")
	assert.False(t, rig.vault.has("listening-x"))
	assert.Nil(t, stateOf(t, rig.svc, listening.SourceX).Signed.Account)
}

func TestSignInInTheOwnersTabKeepsTheSessionAndClearsOnlyThatSite(t *testing.T) {
	rig := newSignedRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceX))

	got, err := rig.svc.SignInCheck(t.Context(), listening.SourceX, "")
	require.NoError(t, err)
	assert.False(t, got.Done)
	assert.Contains(t, got.Waiting, "waiting for its session")

	rig.browser.mu.Lock()
	rig.browser.profile = []browser.Cookie{
		{Name: "auth_token", Value: "token-1", Domain: ".x.com"}, {Name: "ct0", Value: "csrf-1", Domain: ".x.com"},
		{Name: "li_at", Value: "keep-me", Domain: ".linkedin.com"},
	}
	rig.browser.mu.Unlock()

	got, err = rig.svc.SignInCheck(t.Context(), listening.SourceX, "secondary")
	require.NoError(t, err)
	assert.True(t, got.Done)
	assert.True(t, rig.vault.has("listening-x"))
	assert.Equal(t, []string{"li_at"}, rig.browser.profileNames(), "X's cookies leave the shared profile, no other's")

	acct := stateOf(t, rig.svc, listening.SourceX).Signed.Account
	require.NotNil(t, acct)
	assert.Equal(t, "secondary", acct.Label)
	assert.Equal(t, listening.AccountSignedIn, acct.State)

	require.NoError(t, rig.svc.SignOut(t.Context(), listening.SourceX))
	assert.False(t, rig.vault.has("listening-x"))
	assert.Nil(t, stateOf(t, rig.svc, listening.SourceX).Signed.Account)
	assert.Equal(t, []string{"li_at"}, rig.browser.profileNames(), "a sign-out clears that site's cookies only")
}

func TestSignInCheckDoesNotSearchAgainForCookiesFoundSignedOut(t *testing.T) {
	rig := newSignedRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceX))
	rig.x.status = http.StatusUnauthorized

	rig.browser.mu.Lock()
	rig.browser.profile = []browser.Cookie{
		{Name: "auth_token", Value: "stale", Domain: ".x.com"}, {Name: "ct0", Value: "csrf-1", Domain: ".x.com"},
	}
	rig.browser.mu.Unlock()

	for range 3 {
		got, err := rig.svc.SignInCheck(t.Context(), listening.SourceX, "")
		require.NoError(t, err)
		assert.Contains(t, got.Waiting, "does not take the session yet")
	}

	rig.x.mu.Lock()
	defer rig.x.mu.Unlock()

	assert.Equal(t, 1, rig.x.searches, "the dialog polls every two seconds; X is searched once for the same cookies")
}

// linkedInPosts is a search page with posts, each boundary naming its URN.
func linkedInPosts(urns ...string) func(string, int) map[string]any {
	return func(address string, _ int) map[string]any {
		posts := []any{map[string]any{"text": "Promoted\nNo URN here", "urn": ""}}
		for _, u := range urns {
			posts = append(posts, map[string]any{"text": "Someone\n" + u + " says VETC\nPlay\n", "urn": u})
		}

		return map[string]any{"href": address, "status": 200, "ready": "complete", "posts": posts, "empty": false}
	}
}

func signInLinkedIn(t *testing.T, rig *signedRig, page *pageFake) {
	t.Helper()

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceLinkedIn))

	// The sign-in's real search lands on LinkedIn's empty state: the session reads.
	rig.browser.mu.Lock()
	rig.browser.pages["listening-check:linkedin"] = &pageFake{snapshot: func(address string, _ int) map[string]any {
		return map[string]any{"href": address, "status": 200, "ready": "complete", "posts": []any{}, "empty": true}
	}}
	rig.browser.pages["listening:linkedin"] = page
	rig.browser.mu.Unlock()

	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceLinkedIn, "li_at=session-1; JSESSIONID=j", ""))
}

func TestLinkedInReadsTheRenderedPostsSearchAsTheAccount(t *testing.T) {
	rig := newSignedRig(t)

	// 2026-11-25 as a LinkedIn id: milliseconds in its high bits.
	day := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	urn := fmt.Sprintf("urn:li:activity:%d", day.UnixMilli()<<22)
	page := &pageFake{snapshot: linkedInPosts(urn)}
	signInLinkedIn(t, rig, page)

	a := rig.search(t, listening.SourceLinkedIn)

	cov := coverageOf(t, a, listening.SourceLinkedIn)
	assert.Equal(t, listening.BackendRenderedPage, cov.Streams[0].ServedBy)
	require.Len(t, a.Items, 1, "a boundary that names no post gives no item")
	assert.Equal(t, "https://www.linkedin.com/feed/update/"+urn+"/", a.Items[0].URL)
	assert.Equal(t, day, a.Items[0].PublishedAt.UTC())
	assert.NotContains(t, a.Items[0].Text, "Play", "the media player's lines are left out")

	rig.browser.mu.Lock()
	defer rig.browser.mu.Unlock()

	require.NotEmpty(t, page.visits)
	assert.Equal(t, "https://www.linkedin.com/search/results/content/?keywords=VETC&origin=FACETED_SEARCH"+
		"&datePosted=%5B%22past-month%22%5D", page.visits[0])
	assert.Equal(t, "session-1", rig.browser.made["listening:linkedin"][0].Value,
		"the account's tab is made in its own context with its cookies")
}

func TestLinkedInBarriersAreReadFromWhereThePageLanded(t *testing.T) {
	cases := []struct {
		name  string
		href  string
		state int
		code  listening.Code
		acct  string
	}{
		{"challenge", "https://www.linkedin.com/checkpoint/challenge/abc", 200, listening.CodeRefused,
			listening.AccountRefused},
		{"restriction", "https://www.linkedin.com/flagship-web/login/login-restriction/", 200, listening.CodeRefused,
			listening.AccountRefused},
		{"sign-in", "https://www.linkedin.com/login?session_redirect=x", 200, listening.CodeNeedLogin,
			listening.AccountNeedLogin},
		{"throttled", "https://www.linkedin.com/search/results/content/", 999, listening.CodeRateLimited,
			listening.AccountSignedIn},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newSignedRig(t)
			page := &pageFake{snapshot: func(string, int) map[string]any {
				return map[string]any{"href": c.href, "status": c.state, "ready": "complete", "posts": []any{}}
			}}
			signInLinkedIn(t, rig, page)

			a := rig.search(t, listening.SourceLinkedIn)

			cov := coverageOf(t, a, listening.SourceLinkedIn)
			assert.Equal(t, string(c.code), cov.Streams[0].Tried[0].Result)
			assert.Equal(t, listening.BackendSiteSearch, cov.Streams[0].Tried[1].Backend, "the web serves meanwhile")

			acct := stateOf(t, rig.svc, listening.SourceLinkedIn).Signed.Account
			assert.Equal(t, c.acct, acct.State)

			if c.code == listening.CodeRateLimited {
				assert.Equal(t, start.Add(24*time.Hour),
					stateOf(t, rig.svc, listening.SourceLinkedIn).Signed.StoppedUntil, "a rate-limit page stops it for a day")

				visits := len(page.visits)
				rig.clock.Advance(23 * time.Hour)
				_ = rig.search(t, listening.SourceLinkedIn)
				assert.Len(t, page.visits, visits, "not read again within the day")
			}
		})
	}
}

func TestLinkedInWithNoPostAndNoEmptyStateIsNotAZero(t *testing.T) {
	rig := newSignedRig(t)
	page := &pageFake{snapshot: func(address string, _ int) map[string]any {
		return map[string]any{"href": address, "status": 200, "ready": "complete", "posts": []any{}, "empty": false}
	}}
	signInLinkedIn(t, rig, page)

	a := rig.search(t, listening.SourceLinkedIn)

	cov := coverageOf(t, a, listening.SourceLinkedIn)
	assert.Equal(t, string(listening.CodeBroken), cov.Streams[0].Tried[0].Result,
		"a page that rendered no post and no empty state was not read")
}

func TestPacerReadAddsACooldownAfterEveryFiveReads(t *testing.T) {
	c := newClock()

	var waits []time.Duration

	p := listening.NewPacer(c.Now, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		c.Advance(d)

		return nil
	})

	cadence := listening.Cadence{Gap: 10 * time.Second, Every: 5, Cooldown: 30 * time.Second}
	for range 7 {
		done, err := p.Read(t.Context(), "x.com", cadence)
		require.NoError(t, err)
		done()
	}

	assert.Equal(t, []time.Duration{
		10 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second, 40 * time.Second, 10 * time.Second,
	}, waits, "the sixth read waits the gap and the cooldown")
}
