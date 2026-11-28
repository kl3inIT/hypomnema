package clientapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// listeningOver wires social listening as the control plane does: the owner's test reads with no web search here.
type listeningOver struct{ svc *listening.Service }

func (l listeningOver) States(ctx context.Context) ([]listening.SourceState, error) {
	return l.svc.States(ctx)
}

func (l listeningOver) SetSource(ctx context.Context, source string, on bool) error {
	return l.svc.SetSource(ctx, source, on)
}

func (l listeningOver) SetOrder(ctx context.Context, source string, backends []string) error {
	return l.svc.SetOrder(ctx, source, backends)
}

func (l listeningOver) Test(ctx context.Context, source string) error {
	return l.svc.Test(ctx, source, nil, true)
}

func (l listeningOver) Consent(ctx context.Context, source string) error {
	return l.svc.Consent(ctx, source)
}

func (l listeningOver) SignInStart(ctx context.Context, source string) error {
	return l.svc.SignInStart(ctx, source)
}

func (l listeningOver) SignInCheck(ctx context.Context, source, label string) (listening.SignIn, error) {
	return l.svc.SignInCheck(ctx, source, label)
}

func (l listeningOver) PasteCookies(ctx context.Context, source, header, label string) error {
	return l.svc.PasteCookies(ctx, source, header, label)
}

func (l listeningOver) SignOut(ctx context.Context, source string) error {
	return l.svc.SignOut(ctx, source)
}

// noListeningSecrets is a vault that keeps nothing: these calls never reach it.
type noListeningSecrets struct{}

func (noListeningSecrets) Create(context.Context, vault.NewSecret) (vault.Secret, error) {
	return vault.Secret{}, vault.ErrConflict
}

func (noListeningSecrets) Open(context.Context, string) (vault.Opened, error) {
	return vault.Opened{}, vault.ErrNotFound
}

func (noListeningSecrets) Rotate(context.Context, string, vault.Payload) (vault.Secret, error) {
	return vault.Secret{}, vault.ErrNotFound
}

func (noListeningSecrets) Delete(context.Context, string) error { return vault.ErrNotFound }

// The signed-in sources' API (issue #1291): only the owner agrees to a consent card, which switches the source on and
// shows when; a pasted cookie header without the login cookies is refused naming what is missing, never a value.
func TestTheOwnerConsentsToASignedInSourceAndPastedCookiesAreChecked(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := listening.New(listening.Config{
		DB: db, Platforms: listening.SignedSources(listening.Endpoints{}, nil), Secrets: noListeningSecrets{},
	})
	w.srv.WithListening(listeningOver{svc: svc})

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	denied, err := pi.ConsentListeningSourceWithResponse(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, denied.StatusCode(), "only the owner")

	before, err := owner.GetListeningWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, before.StatusCode(), string(before.Body))
	x := before.JSON200.Sources[0]
	require.NotNil(t, x.Signed)
	assert.False(t, x.On)
	assert.Nil(t, x.Signed.ConsentedAt)
	assert.Nil(t, x.Signed.SignInUrl, "no shared browser here: only a pasted cookie header signs in")

	consented, err := owner.ConsentListeningSourceWithResponse(ctx, "x")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, consented.StatusCode(), string(consented.Body))
	assert.True(t, consented.JSON200.Sources[0].On)
	assert.NotNil(t, consented.JSON200.Sources[0].Signed.ConsentedAt)
	assert.False(t, consented.JSON200.Sources[1].On, "LinkedIn needs its own yes")

	pasted, err := owner.PasteListeningCookiesWithResponse(ctx, "x",
		openapi.ListeningCookies{Cookies: "auth_token=do-not-echo"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, pasted.StatusCode())
	assert.Contains(t, string(pasted.Body), "ct0")
	assert.NotContains(t, string(pasted.Body), "do-not-echo")

	started, err := owner.StartListeningSignInWithResponse(ctx, "linkedin")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, started.StatusCode(), "no sign-in before LinkedIn's consent")
}

// The Social listening page's API (issue #1290): the owner reads the sources with their counted groups, switches one
// off, sets a source's backend order and tests one now; another token may not.
func TestTheOwnerSeesAndChangesTheListeningSources(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	refused := true
	item := listening.Item{NativeID: "p1", URL: "https://example.com/p1", Title: "python", PublishedAt: &now}
	svc := listening.New(listening.Config{
		DB: db, Now: func() time.Time { return now },
		Sleep: func(context.Context, time.Duration) error { return nil },
		Platforms: []listening.Platform{
			{Name: "web", Adds: "Pages the web search finds.", Backends: []listening.Backend{{
				Name: "web_search", Host: "web",
				Search: func(context.Context, listening.Request) (listening.Result, error) {
					return listening.Result{Items: []listening.Item{item}}, nil
				},
			}}},
			{Name: "reddit", Adds: "Reddit threads.", Backends: []listening.Backend{
				{
					Name: "reddit_json", Host: "reddit",
					Search: func(context.Context, listening.Request) (listening.Result, error) {
						if refused {
							return listening.Result{}, listening.Fail(listening.CodeRefused, "HTTP 403")
						}

						return listening.Result{Items: []listening.Item{item}}, nil
					},
				},
				{
					Name: "web_arctic", Host: "arctic",
					Search: func(context.Context, listening.Request) (listening.Result, error) {
						return listening.Result{}, listening.Fail(listening.CodeBroken, "a page, not JSON")
					},
				},
			}},
		},
	})
	w.srv.WithListening(listeningOver{svc: svc})

	_, err = svc.Search(ctx, listening.Plan{
		Sources: []string{"web", "reddit"}, Subqueries: []listening.Subquery{{Query: "python"}},
	}, listening.Caller{})
	require.NoError(t, err)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	denied, err := pi.GetListeningWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, denied.StatusCode(), "only the owner")

	listed, err := owner.GetListeningWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	assert.Equal(t, openapi.ListeningCounts{Working: 1, Broken: 1}, listed.JSON200.Counts)

	reddit := listed.JSON200.Sources[1]
	rest := now.Add(listening.RefusedRest)
	code := "refused"
	fix := listening.Fix("reddit", listening.CodeRefused, rest)

	assert.Equal(t, "reddit", reddit.Name)
	assert.Equal(t, "Reddit", reddit.Label)
	assert.Equal(t, "Reddit threads.", reddit.Adds)
	assert.Equal(t, openapi.ListeningGroup(listening.GroupBroken), reddit.Group)
	assert.Equal(t, &code, reddit.Code)
	assert.Equal(t, &now, reddit.CheckedAt)
	assert.Nil(t, reddit.LastSuccessAt)
	require.NotNil(t, reddit.LastError)
	assert.Equal(t, openapi.ListeningFailure{
		Code: "refused", Detail: "HTTP 403", Backend: ptr("reddit_json"), At: now, Fix: fix,
	}, *reddit.LastError)
	require.Len(t, reddit.Backends, 2)
	assert.Equal(t, openapi.ListeningBackend{
		Name: "reddit_json", State: openapi.ListeningBackendStateResting, RestingUntil: &rest, RestCode: &code,
		LastError: &openapi.ListeningFailure{
			Code: "refused", Detail: "HTTP 403", Backend: ptr("reddit_json"), At: now, Fix: fix,
		},
	}, reddit.Backends[0])
	assert.Equal(t, 1, listed.JSON200.Sources[0].ItemsThisWeek)

	order, err := owner.SetListeningOrderWithResponse(ctx, "reddit",
		openapi.ListeningOrder{Backends: []string{"web_arctic", "reddit_json"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, order.StatusCode(), string(order.Body))
	assert.Equal(t, "web_arctic", order.JSON200.Sources[1].Backends[0].Name)

	bad, err := owner.SetListeningOrderWithResponse(ctx, "reddit", openapi.ListeningOrder{Backends: []string{"x"}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())

	missing, err := owner.SetListeningSourceWithResponse(ctx, "tiktok", openapi.ListeningSwitch{On: false})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())

	refused = false
	tested, err := owner.TestListeningSourceWithResponse(ctx, "reddit")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, tested.StatusCode(), string(tested.Body))
	assert.Equal(t, openapi.ListeningGroup(listening.GroupWorking), tested.JSON200.Sources[1].Group,
		"the test cleared reddit_json's rest and it read an item")
	assert.Equal(t, openapi.ListeningBackendStateReady, tested.JSON200.Sources[1].Backends[1].State)

	off, err := owner.SetListeningSourceWithResponse(ctx, "web", openapi.ListeningSwitch{On: false})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, off.StatusCode(), string(off.Body))
	assert.Equal(t, openapi.ListeningCounts{Working: 1, CouldBeOn: 1}, off.JSON200.Counts)
	assert.False(t, off.JSON200.Sources[0].On)

	untested, err := owner.TestListeningSourceWithResponse(ctx, "web")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, untested.StatusCode(), "switch it on to test it")
}
