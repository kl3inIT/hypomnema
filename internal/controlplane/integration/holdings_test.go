package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
)

// checking wires what the control plane wires (issue #567): what the account
// holds, said action by action from the proxy's catalog, kept on the
// Connection.
func (w *world) checking() *world {
	w.svc.WithChecker(func(ctx context.Context, id, label string) error {
		held, err := w.svc.Holdings(ctx, id, label)
		if err != nil {
			return err
		}

		return w.orch.SetConnectionAccess(ctx, id, label, serviceproxy.Assess(held, w.clock.Now()))
	})

	return w
}

func accessOf(c integration.Connection, action string) orchestration.ActionAccess {
	return c.Access.Of(action)
}

// Scenario: the owner signs his Google account in for Gmail to read and
// Calendar to read and write. What it can do is read from the grants the
// sign-in gave, as it ends: reading mail is granted, sending is missing
// because Gmail was granted read only, and Drive, which was not asked for, is
// missing. Nothing more is asked of Google.
func TestAGoogleAccountCanDoWhatItsSignInGranted(t *testing.T) {
	t.Parallel()

	w := newWorld(t).withClient().checking()

	conn, err := w.signIn("work", "gmail:read", "calendar")
	require.NoError(t, err)

	assert.Equal(t, orchestration.ActionAccess{Access: "granted", Reason: "granted gmail:read"},
		accessOf(conn, "gmail.read"))
	assert.Equal(t, orchestration.ActionAccess{Access: "missing", Reason: "gmail granted read only"},
		accessOf(conn, "gmail.send"))
	assert.Equal(t, "granted", accessOf(conn, "calendar.write").Access)
	assert.Equal(t, orchestration.ActionAccess{Access: "missing", Reason: "drive not granted at sign-in"},
		accessOf(conn, "drive.read"))
	assert.Equal(t, orchestration.ActionAccess{Access: "missing", Reason: "docs or sheets not granted at sign-in"},
		accessOf(conn, "docs.read"))
	assert.Equal(t, orchestration.ActionAccess{Access: "missing", Reason: "searchconsole not granted at sign-in"},
		accessOf(conn, "searchconsole.read"))
	assert.Equal(t, t0, conn.Access.CheckedAt)

	listed := w.theGoogle().Connections[0]
	assert.Equal(t, conn.Access, listed.Access, "it is kept on the Connection")
}

// Scenario: the owner connects his Lark app. What it can do is the app's
// enabled scopes, read from Lark as lark-cli's `auth scopes` reads them, the
// tenant ones: with im:message:readonly and nothing for mail, reading
// messages is granted and reading mail missing; a part lark-cli names no
// scope for is unknown. When Lark will not say, every action is unknown and
// nothing is refused for it.
func TestALarkAppCanDoWhatItsScopesAllow(t *testing.T) {
	t.Parallel()

	w, l := larkWorld(t)
	w.checking()

	refuse := false
	l.mux.HandleFunc("GET /apps/{id}", func(rw http.ResponseWriter, r *http.Request) {
		if refuse || r.PathValue("id") != appID || r.URL.Query().Get("lang") == "" {
			l.answer(rw, http.StatusBadRequest, map[string]any{"code": 99991672, "msg": "no scope"})

			return
		}

		l.answer(rw, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"app": map[string]any{
			"scopes": []any{
				map[string]any{"scope": "im:message:readonly", "token_types": []string{"tenant", "user"}},
				map[string]any{"scope": "mail:user_mailbox.message:readonly", "token_types": []string{"user"}},
			},
		}}})
	})

	conn, err := w.svc.Connect(t.Context(), integration.LarkID, "work", integration.App{ID: appID, Secret: appSecret})
	require.NoError(t, err)

	read := accessOf(conn, "lark.im.read")
	assert.Equal(t, "granted", read.Access)
	assert.Contains(t, read.Reason, "has im:message:readonly")

	mail := accessOf(conn, "lark.mail.read")
	assert.Equal(t, "missing", mail.Access, "a user scope is not the app's")
	assert.True(t, strings.HasPrefix(mail.Reason, "the app has none of the scopes these calls need, such as "),
		mail.Reason)
	assert.Equal(t, "unknown", accessOf(conn, "lark.other.read").Access)

	refuse = true

	conn, err = w.svc.Check(t.Context(), integration.LarkID, "work")
	require.NoError(t, err)
	assert.Equal(t, orchestration.ActionAccess{
		Access: "unknown", Reason: "Lark did not say which scopes the app has: status 400, code 99991672",
	}, accessOf(conn, "lark.im.read"))
	assert.Zero(t, conn.Access.Missing())
}

// Scenario: the owner signs in to Lark as himself. What his sign-in can do is
// the scope Lark granted the token, kept with it and read against each
// method's user scopes; a sign-in whose token names only offline_access can
// do nothing that names a scope.
func TestTheOwnersLarkSignInCanDoWhatItsScopeSays(t *testing.T) {
	t.Parallel()

	w, _, accounts, _ := larkUserWorld(t)
	w.checking()
	accounts.granted = "offline_access im:message:readonly im:chat:readonly"

	me, err := w.signInAsOwner("me")
	require.NoError(t, err)

	assert.Equal(t, "granted", accessOf(me, "lark.im.read").Access)
	assert.Equal(t, "missing", accessOf(me, "lark.calendar.read").Access)
	assert.Contains(t, accessOf(me, "lark.calendar.read").Reason, "the sign-in was granted none of the scopes")

	accounts.granted = ""

	me, err = w.signInAsOwner("me")
	require.NoError(t, err)
	assert.Equal(t, "missing", accessOf(me, "lark.im.read").Access, "offline_access alone reads nothing")
}

// Scenario (seen on zm, 2026-11-15): the owner connects Superset with an
// account whose one role is ServiceReader. Its roles are read from
// /api/v1/me/roles/: dashboards, charts and datasets are granted; queries,
// saved queries, logs, users and roles, row level security, annotations,
// SQL Lab and every change are missing, each with the role and the
// permission it lacks; the account's own part is unknown. An account that
// may not read its roles is unknown everywhere.
func TestASupersetAccountCanDoWhatItsRolesAllow(t *testing.T) {
	t.Parallel()

	w, s := supersetWorld(t)
	w.checking()
	s.SetRoles(supersettest.ServiceReaderRoles)

	conn, err := w.connectSuperset(s, supersettest.Password)
	require.NoError(t, err)

	for action, reason := range map[string]string{
		"superset.dashboards.read": "role ServiceReader has can_read on Dashboard",
		"superset.charts.read":     "role ServiceReader has can_read on Chart",
		"superset.datasets.read":   "role ServiceReader has can_read on Dataset",
		"superset.tags.read":       "role ServiceReader has can_read on Tag",
	} {
		assert.Equal(t, orchestration.ActionAccess{Access: "granted", Reason: reason}, accessOf(conn, action), action)
	}

	for action, reason := range map[string]string{
		"superset.queries.read":     "role ServiceReader has no can_read on Query or SavedQuery",
		"superset.logs.read":        "role ServiceReader has no can_read on Log",
		"superset.security.read":    "role ServiceReader has no can_read on User or Role",
		"superset.rls.read":         "role ServiceReader has no can_read on Row Level Security",
		"superset.annotations.read": "role ServiceReader has no can_read on Annotation",
		"superset.sqllab.read":      "role ServiceReader has no can_read on SQLLab",
		"superset.sql.run":          "role ServiceReader has no can_execute_sql_query on SQLLab",
		"superset.assets.read":      "role ServiceReader has no can_export on ImportExportRestApi",
		"superset.dashboards.write": "role ServiceReader has no can_write on Dashboard",
		"superset.charts.delete":    "role ServiceReader has no can_write on Chart",
	} {
		assert.Equal(t, orchestration.ActionAccess{Access: "missing", Reason: reason}, accessOf(conn, action), action)
	}

	assert.Equal(t, "unknown", accessOf(conn, "superset.system.read").Access)
	assert.Equal(t, "unknown", accessOf(conn, "superset.other.read").Access)

	s.SetRoles(nil)

	conn, err = w.svc.Check(t.Context(), integration.SupersetID, "vmaps")
	require.NoError(t, err)
	assert.Equal(t, orchestration.ActionAccess{Access: "unknown", Reason: "Superset answered 403 to /api/v1/me/roles/"},
		accessOf(conn, "superset.logs.read"))
	assert.Zero(t, conn.Access.Missing())

	_, err = w.svc.Check(t.Context(), integration.SupersetID, "other")
	require.ErrorIs(t, err, orchestration.ErrNotFound)
}

// Scenario: a check that cannot be made leaves the Connection as it was
// saved: connecting does not fail for it.
func TestAConnectionIsMadeWhenItsAccessCannotBeRead(t *testing.T) {
	t.Parallel()

	w, s := supersetWorld(t)
	w.svc.WithChecker(func(context.Context, string, string) error { return fmt.Errorf("the store is down") })

	conn, err := w.connectSuperset(s, supersettest.Password)
	require.NoError(t, err)
	assert.Equal(t, "unknown", accessOf(conn, "superset.logs.read").Access)

	_, err = w.svc.Check(t.Context(), integration.SupersetID, "vmaps")
	require.ErrorContains(t, err, "the store is down", "the owner's own check says why")
}
