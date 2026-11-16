package clientcli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
)

// Scenario (issue #567, rule 7): the owner lists what agents may do with his
// Google account, granted Gmail read only. Each action has a column for
// whether the account can do it; sending, which it cannot, says why. The
// same is in --json, as access and reason.
func TestTheActionsSayWhatTheAccountCanDo(t *testing.T) {
	t.Parallel()

	w := newWorldWith(t, nil)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "google", Label: "work", Account: "owner@example.com", Secret: "conn-google-work",
	})
	require.NoError(t, err)
	require.NoError(t, w.svc.SetConnectionAccess(ctx, "google", "work", serviceproxy.Assess(integration.Holdings{
		Integration: integration.GoogleID, Grants: []integration.Grant{{Service: "gmail", ReadOnly: true}},
	}, time.Now())))

	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "integrations", "actions", "google:work")
	require.NoError(t, err)
	assert.Contains(t, out, "gmail.read         allow  granted  Gmail: Read mail\n")
	assert.Contains(t, out, "gmail.send         ask    missing  Gmail: Send mail  cannot: gmail granted read only\n")

	raw, err := w.cmd(t, "integrations", "actions", "google:work", "--json")
	require.NoError(t, err)

	var listed openapi.ConnectionActionList
	require.NoError(t, json.Unmarshal([]byte(raw), &listed))

	for _, a := range listed.Data {
		if a.Id == "gmail.send" {
			assert.Equal(t, openapi.ActionAccessMissing, a.Access)
			assert.Equal(t, "gmail granted read only", val(a.Reason))
		}
	}
}

// Scenario (issue #567, rule 6): the owner checks his Google account again.
// The command reads it at the service and lists what it cannot do; the
// account's line in the list counts them. Only the owner may check.
func TestTheOwnerChecksAnAccountAgain(t *testing.T) {
	t.Parallel()

	services := &signIns{
		clientID: "client-1.apps.googleusercontent.com",
		connections: []integration.Connection{{
			Integration: integration.GoogleID, Label: "work", Account: "owner@example.com",
			Grants: []integration.Grant{{Service: "gmail", ReadOnly: true}},
		}},
		access: serviceproxy.Assess(integration.Holdings{
			Integration: integration.GoogleID, Grants: []integration.Grant{{Service: "gmail", ReadOnly: true}},
		}, time.Now()),
	}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithIntegrations(services) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "integrations", "check", "google:work")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "google:work: the account cannot do 17 actions; a request for one is "+
		"refused before it is sent\n"), out)
	assert.Contains(t, out, "gmail.send         Gmail: Send mail  cannot: gmail granted read only\n")
	assert.Contains(t, out, "drive.read         Drive: Read files  cannot: drive not granted at sign-in\n")
	assert.NotContains(t, out, "gmail.read")

	raw, err := w.cmd(t, "integrations", "check", "google:work", "--json")
	require.NoError(t, err)

	var checked openapi.IntegrationConnection
	require.NoError(t, json.Unmarshal([]byte(raw), &checked))
	assert.Len(t, checked.Cannot, 17)
	assert.NotNil(t, checked.AccessCheckedAt)

	listed, err := w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, listed, "work (owner@example.com: gmail:read) cannot 17")

	_, err = w.cmd(t, "integrations", "check", "google:other")
	require.Error(t, err)

	w.login(t, bearertoken.Coordinator)

	_, err = w.cmd(t, "integrations", "check", "google:work")
	require.ErrorContains(t, err, "only the owner")
}

// Scenario (seen on zm, 2026-11-15): the owner connects a Superset account
// whose one role is ServiceReader. Its roles are read at once. A request to
// Superset's logs, which the role cannot read, is refused before it is sent,
// with why, and the dashboards are listed with what the account cannot do.
func TestASupersetAccountThatCannotReadLogsIsRefusedBeforeSuperset(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t, 200*time.Millisecond)
	w.integrations.WithChecker(func(ctx context.Context, id, label string) error {
		held, err := w.integrations.Holdings(ctx, id, label)
		if err != nil {
			return err
		}

		return w.svc.SetConnectionAccess(ctx, id, label, serviceproxy.Assess(held, time.Now()))
	})
	w.superset.SetRoles(supersettest.ServiceReaderRoles)
	w.connect(t)

	_, err := w.cmd(t, "superset", "api", "GET", "/api/v1/log/")
	require.ErrorContains(t, err, `Refused: hypomnema-reader cannot "Logs: Read" at superset:vmaps: role `+
		"ServiceReader has no can_read on Log.")

	for _, r := range w.superset.Requests() {
		assert.NotEqual(t, "/api/v1/log/", r.Path, "the refused read never reached Superset")
	}

	out, err := w.cmd(t, "superset", "dashboards")
	require.NoError(t, err)
	assert.Contains(t, out, "9\tProduct usage\n")
	assert.Contains(t, out, "superset:vmaps cannot do these; a request for one is refused before it is sent:\n")
	assert.Contains(t, out, "  Logs: Read (role ServiceReader has no can_read on Log)")
	assert.NotContains(t, out, "Dashboards: Read")

	actions, err := w.cmd(t, "integrations", "actions", "superset:vmaps")
	require.NoError(t, err)
	assert.Contains(t, actions, "superset.dashboards.read allow  granted  Dashboards: Read\n")
}
