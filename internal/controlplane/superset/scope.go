package superset

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scope is the dashboards a caller may read of a Connection (issue #562): a
// Bot the owner limited to some of them. Not Limited, it reads every
// dashboard the account sees, as before. Limited, it reads only through the
// reads here (a chart read with its dashboard's filters set, and a filter's
// values, issue #648), whose requests are built from the saved dashboard and
// chart, never through a request it writes (Relay is refused): as
// OpenMausBot refuses its generic proxy to a scoped bot
// (packages/phone-protocol/src/connector-scopes.ts L37, L73) and Superset's
// guest token is held to the dashboards it names and their charts
// (superset/security/manager.py: has_guest_access, "slc in
// dashboard_.slices", and query_context_modified). What is allowed is read
// from Superset on every call, as Superset checks the dashboard's charts on
// every request: no list is kept here.
//
// The client API's commands and the agent's tools each take their caller's
// Scope and read through it, so the rule is the same for both.
type Scope struct {
	Limited    bool
	Dashboards []int64
}

// integrationID is the Integration whose Connections these are.
const integrationID = "superset"

// ScopeOf is the dashboards a Bot may read of a Superset Connection.
func ScopeOf(bot orchestration.Bot, label string) Scope {
	scope, limited := bot.ScopeOf(orchestration.Use(integrationID, label))

	return Scope{Limited: limited, Dashboards: scope.Dashboards}
}

// errOutOfScope marks a refusal of what the scope leaves out, for the log.
// The caller is answered as for a dashboard that does not exist, as
// Superset's DashboardAccessFilter leaves a dashboard out of what a user
// sees (superset/dashboards/filters.py).
var errOutOfScope = errors.New("out of scope")

// errNoChart marks a chart that is not on the dashboard named.
var errNoChart = errors.New("no such chart on the dashboard")

// OutOfScope says the error is a refusal of what the scope leaves out.
func OutOfScope(err error) bool {
	return errors.Is(err, errOutOfScope)
}

func dashboardNotFound(ref string, out bool) error {
	if out {
		return fmt.Errorf("%w: dashboard %s not found%w", orchestration.ErrNotFound, ref, quiet{errOutOfScope})
	}

	return fmt.Errorf("%w: dashboard %s not found", orchestration.ErrNotFound, ref)
}

// quiet wraps an error so that it is matched by errors.Is and adds nothing
// to the message.
type quiet struct{ err error }

func (quiet) Error() string   { return "" }
func (q quiet) Unwrap() error { return q.err }

func (s Scope) allows(id int64) bool {
	return !s.Limited || slices.Contains(s.Dashboards, id)
}

// names is the scope's dashboards as a refusal names them.
func (s Scope) names() string {
	if len(s.Dashboards) == 0 {
		return "none"
	}

	out := make([]string, 0, len(s.Dashboards))
	for _, d := range s.Dashboards {
		out = append(out, strconv.FormatInt(d, 10))
	}

	return strings.Join(out, ", ")
}

// RelayRefused is the refusal of a request the caller writes itself, for a
// limited caller, naming the dashboards it may read; nil for one that is not
// limited.
func (s Scope) RelayRefused(connection string) error {
	if !s.Limited {
		return nil
	}

	return fmt.Errorf("%w: this bot reads %s only through its dashboards (%s): list them, read one, read a "+
		"chart's data with its filters, list a filter's values; it may not send its own requests%w",
		orchestration.ErrForbidden, connection, s.names(), quiet{errOutOfScope})
}

// ListDashboards lists the dashboards whose title contains search, only those
// of the scope.
func (s Scope) ListDashboards(ctx context.Context, relay Relay, search string) ([]DashboardSummary, error) {
	found, err := Dashboards(ctx, relay, search)
	if err != nil || !s.Limited {
		return found, err
	}

	return slices.DeleteFunc(found, func(d DashboardSummary) bool { return !s.allows(d.ID) }), nil
}

// Dashboard returns a dashboard of the scope, named by its number or its
// slug. One outside it is answered as one that does not exist; a slug is
// compared by the number Superset answers for it.
func (s Scope) Dashboard(ctx context.Context, relay Relay, ref string) (Dashboard, error) {
	if !s.Limited {
		return GetDashboard(ctx, relay, ref)
	}

	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && !s.allows(id) {
		return Dashboard{}, dashboardNotFound(ref, true)
	}

	d, err := GetDashboard(ctx, relay, ref)

	switch {
	case errors.Is(err, orchestration.ErrNotFound):
		return Dashboard{}, dashboardNotFound(ref, false)
	case err != nil:
		return Dashboard{}, err
	case !s.allows(d.ID):
		return Dashboard{}, dashboardNotFound(ref, true)
	}

	return d, nil
}

// ChartData returns a chart's rows of a dashboard of the scope. The
// dashboard is checked before any query runs: a slug is first asked for its
// number. The chart must be one of the dashboard's (GetChartData).
func (s Scope) ChartData(ctx context.Context, relay Relay, ref, chart string, filters Filters, now time.Time,
) (ChartData, error) {
	if !s.Limited {
		return GetChartData(ctx, relay, ref, chart, filters, now)
	}

	id, err := s.dashboardID(ctx, relay, ref)
	if err != nil {
		return ChartData{}, err
	}

	data, err := GetChartData(ctx, relay, strconv.FormatInt(id, 10), chart, filters, now)
	if errors.Is(err, orchestration.ErrNotFound) && !errors.Is(err, errNoChart) {
		return ChartData{}, dashboardNotFound(ref, false)
	}

	return data, err
}

// dashboardID is the number of a dashboard of the scope.
func (s Scope) dashboardID(ctx context.Context, relay Relay, ref string) (int64, error) {
	if err := checkID("dashboard", ref); err != nil {
		return 0, err
	}

	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		var answer dashboardAnswer

		err := call(ctx, relay, http.MethodGet, "/api/v1/dashboard/"+ref, nil, &answer)

		switch {
		case errors.Is(err, orchestration.ErrNotFound):
			return 0, dashboardNotFound(ref, false)
		case err != nil:
			return 0, err
		}

		id = answer.Result.ID
	}

	if !s.allows(id) {
		return 0, dashboardNotFound(ref, true)
	}

	return id, nil
}
