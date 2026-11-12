package integration_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// supersetWorld is a world whose service knows Superset, and a stand-in for
// one deployment of it.
func supersetWorld(t *testing.T) (*world, *supersettest.Superset) {
	t.Helper()

	w, s := newWorld(t), supersettest.New(t)
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.Superset())

	return w, s
}

func (w *world) connectSuperset(s *supersettest.Superset, password string) (integration.Connection, error) {
	w.t.Helper()

	return w.svc.ConnectAccount(w.t.Context(), integration.SupersetID, "vmaps", integration.Account{
		Address: s.URL() + "/", User: supersettest.User, Password: password,
	})
}

func (w *world) get(path string) (integration.AccountAnswer, error) {
	w.t.Helper()

	return w.svc.Call(w.t.Context(), integration.SupersetID, "vmaps", integration.AccountRequest{
		Method: http.MethodGet, Path: path,
	})
}

// Scenario: the owner connects his company's Superset as "vmaps" with its
// address, a user name and a password. A wrong password is refused and
// nothing is stored. The right one signs in once: the Connection carries the
// address, the user name and the version Superset says it runs; the password
// is sealed in the vault and nothing else is written. An address that could
// be read two ways, and an Integration of another kind, are refused.
func TestTheOwnerConnectsASupersetAccount(t *testing.T) {
	t.Parallel()

	w, s := supersetWorld(t)

	_, err := w.connectSuperset(s, "a-wrong-password")
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.NotContains(t, err.Error(), "a-wrong-password")
	assert.Empty(t, w.theSuperset().Connections, "a refused sign-in stores nothing")

	secrets, err := w.vault.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, secrets)

	conn, err := w.connectSuperset(s, supersettest.Password)
	require.NoError(t, err)
	assert.Equal(t, supersettest.User, conn.Account)
	assert.Equal(t, s.URL(), conn.Address, "the address is kept without its last slash")
	assert.Equal(t, supersettest.Version, conn.Version)
	assert.False(t, conn.SignInRequired)
	assert.False(t, conn.Chats, "no agent uses a Connection until the owner says so")

	listed := w.theSuperset()
	assert.Equal(t, integration.KindAccount, listed.CredentialKind())
	require.Len(t, listed.Connections, 1)
	assert.Equal(t, conn.Address, listed.Connections[0].Address)

	stored := w.stored()
	assert.NotContains(t, stored, supersettest.Password, "the password is sealed")
	assert.NotContains(t, stored, "access-", "no token is written")
	assert.NotContains(t, stored, "refresh-", "no token is written")

	for _, address := range []string{
		"superset.example.com", "ftp://superset.example.com", "https://u:p@superset.example.com",
		"https://superset.example.com/?next=x", "",
	} {
		_, err = w.svc.ConnectAccount(t.Context(), integration.SupersetID, "other", integration.Account{
			Address: address, User: supersettest.User, Password: supersettest.Password,
		})
		require.ErrorIs(t, err, integration.ErrInvalid, address)
	}

	_, err = w.svc.Connect(t.Context(), integration.SupersetID, "other")
	require.ErrorIs(t, err, integration.ErrInvalid, "Superset is not an app")

	_, err = w.svc.Begin(t.Context(), integration.SupersetID, "other", nil)
	require.ErrorIs(t, err, integration.ErrInvalid, "Superset has no consent page")
}

func (w *world) theSuperset() integration.Integration {
	w.t.Helper()

	all, err := w.svc.List(w.t.Context())
	require.NoError(w.t, err)
	require.Len(w.t, all, 1)

	return all[0]
}

// Scenario: requests are sent to a connected Superset over a working day.
// The control plane signed in once, when the owner connected it. When
// Superset refuses a request because the access token ended, a new one is
// asked for with the refresh token and the request is sent again: the caller
// sees only the answer. When Superset no longer knows the refresh token
// either, the control plane signs in again with the password from the vault.
// A request that is not a GET carries the CSRF token, the session cookie it
// came with and a Referer, asked for once; when Superset no longer takes that
// token, it is asked for again and the request is sent again.
func TestARequestRefusedForItsTokenIsSentAgainWithANewOne(t *testing.T) {
	t.Parallel()

	w, s := supersetWorld(t)
	_, err := w.connectSuperset(s, supersettest.Password)
	require.NoError(t, err)

	answer, err := w.get("/api/v1/dashboard/9")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, answer.Status)
	assert.Contains(t, string(answer.Body), "Product usage")

	logins, refreshes := s.SignIns()
	assert.Equal(t, 1, logins, "the sign-in of connecting is the only one")
	assert.Zero(t, refreshes)

	s.ExpireAccessTokens()

	answer, err = w.get("/api/v1/dashboard/9")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, answer.Status, "refused once, then accepted with a new token")

	logins, refreshes = s.SignIns()
	assert.Equal(t, 1, logins)
	assert.Equal(t, 1, refreshes)

	s.EndSessions()

	answer, err = w.get("/api/v1/dashboard/9")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, answer.Status)

	logins, _ = s.SignIns()
	assert.Equal(t, 2, logins, "the refresh token was refused, so the password signed in again")

	for range 2 {
		answer, err = w.svc.Call(t.Context(), integration.SupersetID, "vmaps", integration.AccountRequest{
			Method: http.MethodPut, Path: "/api/v1/chart/166", ContentType: "application/json",
			Body: []byte(`{"slice_name":"renamed"}`),
		})
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, answer.Status, string(answer.Body))
	}

	s.EndCSRFTokens()

	answer, err = w.svc.Call(t.Context(), integration.SupersetID, "vmaps", integration.AccountRequest{
		Method: http.MethodPut, Path: "/api/v1/chart/166", ContentType: "application/json",
		Body: []byte(`{"slice_name":"renamed"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, answer.Status, "refused for its CSRF token once, then sent again with a new one")

	var puts, csrfAsked int

	for _, r := range s.Requests() {
		switch {
		case r.Method == http.MethodPut:
			puts++

			assert.NotEmpty(t, r.CSRF)
			assert.Contains(t, r.Cookie, "session=")
			assert.Equal(t, s.URL(), r.Referer)
		case r.Path == "/api/v1/security/csrf_token/":
			csrfAsked++
		default:
			assert.Empty(t, r.CSRF, "a GET carries no CSRF token")
		}
	}

	assert.Equal(t, 4, puts, "three requests, one of them sent twice")
	assert.Equal(t, 2, csrfAsked, "the CSRF token is kept, and asked for again only when it is refused")

	// A request Superset itself refuses is the caller's answer, not a reason to sign in again.
	answer, err = w.get("/api/v1/chart/999")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, answer.Status)

	logins, _ = s.SignIns()
	assert.Equal(t, 2, logins)
}

// Scenario: the account's password is changed at Superset. The next request
// cannot sign in: the caller is told the account must be connected again,
// and the owner sees it on the Connection. He connects it again with the new
// password and requests work; disconnecting forgets the Connection and its
// password.
func TestAPasswordChangedAtSupersetAsksToConnectAgain(t *testing.T) {
	t.Parallel()

	w, s := supersetWorld(t)
	_, err := w.connectSuperset(s, supersettest.Password)
	require.NoError(t, err)

	s.SetPassword("a-new-password-at-superset")
	s.EndSessions()

	_, err = w.get("/api/v1/dashboard/9")
	require.ErrorIs(t, err, integration.ErrSignInRequired)
	assert.NotContains(t, err.Error(), supersettest.Password)
	assert.True(t, w.theSuperset().Connections[0].SignInRequired, "the owner sees the account stopped working")

	_, err = w.svc.Token(t.Context(), integration.SupersetID, "vmaps")
	require.ErrorIs(t, err, integration.ErrSignInRequired)

	conn, err := w.connectSuperset(s, "a-new-password-at-superset")
	require.NoError(t, err)
	assert.False(t, conn.SignInRequired)

	answer, err := w.get("/api/v1/dashboard/9")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, answer.Status)

	_, err = w.svc.Disconnect(t.Context(), integration.SupersetID, "vmaps")
	require.NoError(t, err)
	assert.Empty(t, w.theSuperset().Connections)

	secrets, err := w.vault.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, secrets, "the password is forgotten with the Connection")

	_, err = w.get("/api/v1/dashboard/9")
	require.ErrorIs(t, err, orchestration.ErrNotFound)
}
