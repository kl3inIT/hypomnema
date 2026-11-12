package serviceproxy_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// Scenario: the owner opens the actions of a Superset account. They are the
// parts of Superset's own API, each with at most a read, a change and a
// delete: reading runs on its own, the rest asks. A chart's data is a read
// though it is a POST; running SQL is an action of its own; so is
// administering users and roles; a method a newer Superset has is the read,
// the change or the delete of its part of the API; a request outside the API
// is no action and always asks. No action names a host: a Superset is at its
// Connection's own address, and no tunnel is opened to one.
func TestSupersetsActionsAreItsOwnAPIWithReadingAllowedAndTheRestAsked(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf(serviceproxy.SupersetID)
	require.True(t, ok)

	listed := catalog.Listed()
	assert.Len(t, listed, 41)
	assert.Equal(t, "Dashboards", listed[0].Service)

	var dashboards []string

	for _, a := range listed {
		assert.Equal(t, a.Kind == serviceproxy.Reads, a.Default() == orchestration.PolicyAllow, a.ID)

		for _, r := range a.Routes {
			assert.Empty(t, r.Host, a.ID)
		}

		if a.Service == "Dashboards" {
			dashboards = append(dashboards, a.ID)
		}
	}

	assert.Equal(t, []string{"superset.dashboards.read", "superset.dashboards.write", "superset.dashboards.delete"},
		dashboards)
	assert.False(t, catalog.Has("superset.example.com"))

	//nolint:gosec // Paths of Superset's API and the names of actions; no credential is here.
	for request, action := range map[string]string{
		"GET /api/v1/dashboard/":                                 "superset.dashboards.read",
		"GET /api/v1/dashboard/9/charts":                         "superset.dashboards.read",
		"PUT /api/v1/dashboard/9":                                "superset.dashboards.write",
		"DELETE /api/v1/dashboard/9":                             "superset.dashboards.delete",
		"POST /api/v1/dashboard/import/":                         "superset.dashboards.write",
		"GET /api/v1/chart/166":                                  "superset.charts.read",
		"GET /api/v1/chart/166/data/":                            "superset.charts.read",
		"POST /api/v1/chart/data":                                "superset.charts.read",
		"POST /api/v1/chart/":                                    "superset.charts.write",
		"DELETE /api/v1/chart/166":                               "superset.charts.delete",
		"GET /api/v1/dataset/27":                                 "superset.datasets.read",
		"GET /api/v1/database/1/tables/":                         "superset.databases.read",
		"POST /api/v1/database/1/validate_sql/":                  "superset.sql.run",
		"POST /api/v1/sqllab/execute/":                           "superset.sql.run",
		"POST /api/v1/sqllab/estimate/":                          "superset.sql.run",
		"POST /api/v1/sqllab/format_sql/":                        "superset.sqllab.read",
		"GET /api/v1/sqllab/results/":                            "superset.sqllab.read",
		"GET /api/v1/security/users/":                            "superset.security.read",
		"POST /api/v1/security/users/":                           "superset.security.admin",
		"DELETE /api/v1/security/roles/3":                        "superset.security.admin",
		"POST /api/v1/security/guest_token/":                     "superset.security.admin",
		"POST /api/v1/security/login":                            "superset.security.admin",
		"GET /api/v1/me/":                                        "superset.system.read",
		"GET /api/v1/_openapi":                                   "superset.system.read",
		"GET /api/v1/chart/166/something_new":                    "superset.charts.read",
		"POST /api/v1/chart/166/something_new":                   "superset.charts.write",
		"GET /api/v1/a_new_part/1":                               "superset.other.read",
		"POST /api/v1/a_new_part/":                               "superset.unlisted",
		"POST /superset/sql_json/":                               "superset.unlisted",
		"GET /login/":                                            "superset.unlisted",
		"POST /api/v1/dashboard/9/cache_dashboard_screenshot/":   "superset.dashboards.read",
		"POST /api/v1/dashboard/9/filter_state":                  "superset.dashboards.write",
		"PUT /api/v1/dataset/27/refresh":                         "superset.datasets.write",
		"DELETE /api/v1/dataset/27/column/3":                     "superset.datasets.delete",
		"POST /api/v1/assets/import/":                            "superset.assets.write",
		"GET /api/v1/assets/export/":                             "superset.assets.read",
		"POST /api/v1/cachekey/invalidate":                       "superset.system.write",
		"GET /api/v1/rowlevelsecurity/":                          "superset.rls.read",
		"POST /api/v1/rowlevelsecurity/":                         "superset.rls.write",
		"GET /api/v1/datasource/table/27/column/unit/values/":    "superset.datasets.read",
		"GET /api/v1/embedded_dashboard/0a1b":                    "superset.dashboards.read",
		"POST /api/v1/explore/form_data":                         "superset.charts.write",
		"GET /api/v1/query/":                                     "superset.queries.read",
		"POST /api/v1/query/stop":                                "superset.queries.write",
		"DELETE /api/v1/saved_query/4":                           "superset.queries.delete",
		"GET /api/v1/report/":                                    "superset.reports.read",
		"POST /api/v1/annotation_layer/1/annotation/":            "superset.annotations.write",
		"POST /api/v1/tag/":                                      "superset.tags.write",
		"PUT /api/v1/css_template/1":                             "superset.css.write",
		"GET /api/v1/log/":                                       "superset.logs.read",
		"POST /api/v1/database/":                                 "superset.databases.write",
		"DELETE /api/v1/database/1":                              "superset.databases.delete",
		"POST /api/v1/database/test_connection/":                 "superset.databases.write",
		"GET /api/v1/database/1/select_star/people/":             "superset.databases.read",
		"POST /api/v1/dashboard/9/copy/":                         "superset.dashboards.write",
		"DELETE /api/v1/dashboard/9/embedded":                    "superset.dashboards.delete",
		"POST /api/v1/chart/166/favorites/":                      "superset.charts.write",
		"PUT /api/v1/chart/warm_up_cache":                        "superset.charts.write",
		"GET /api/v1/chart/166/cache_screenshot/":                "superset.charts.read",
		"DELETE /api/v1/explore/form_data/k":                     "superset.charts.delete",
		"GET /api/v1/advanced_data_type/types":                   "superset.system.read",
		"GET /api/v1/user/1/avatar.png":                          "superset.system.read",
		"POST /api/v1/log/":                                      "superset.logs.write",
		"DELETE /api/v1/security/users/3":                        "superset.security.admin",
		"PUT /api/v1/security/roles/3":                           "superset.security.admin",
		"GET /api/v1/security/csrf_token/":                       "superset.security.read",
		"GET /api/v1/dashboard/product-usage":                    "superset.dashboards.read",
		"POST /api/v1/dataset/get_or_create/":                    "superset.datasets.write",
		"POST /api/v1/dataset/duplicate":                         "superset.datasets.write",
		"GET /api/v1/dataset/distinct/schema":                    "superset.datasets.read",
		"GET /api/v1/tag/get_objects/":                           "superset.tags.read",
		"DELETE /api/v1/tag/chart/166/important/":                "superset.tags.delete",
		"DELETE /api/v1/report/2":                                "superset.reports.delete",
		"DELETE /api/v1/annotation_layer/1":                      "superset.annotations.delete",
		"DELETE /api/v1/css_template/1":                          "superset.css.delete",
		"DELETE /api/v1/rowlevelsecurity/1":                      "superset.rls.delete",
		"GET /api/v1/saved_query/export/":                        "superset.queries.read",
		"GET /api/v1/sqllab/export/abc/":                         "superset.sqllab.read",
		"GET /api/v1/database/1/schemas_access_for_file_upload/": "superset.databases.read",
		"POST /api/v1/database/1/csv_upload/":                    "superset.databases.write",
		"DELETE /api/v1/database/1/ssh_tunnel/":                  "superset.databases.delete",
		"POST /api/v1/database/validate_parameters/":             "superset.databases.write",
		"GET /api/v1/async_event/":                               "superset.system.read",
		"GET /api/v1/menu/":                                      "superset.system.read",
		"GET /api/v1/available_domains/":                         "superset.system.read",
		"DELETE /api/v1/a_new_part/1":                            "superset.unlisted",
		"PUT /api/v1/dashboard/9/filter_state/k":                 "superset.dashboards.write",
		"DELETE /api/v1/dashboard/9/filter_state/k":              "superset.dashboards.delete",
		"POST /api/v1/dashboard/9/permalink":                     "superset.dashboards.write",
		"GET /api/v1/dashboard/permalink/k":                      "superset.dashboards.read",
		"POST /api/v1/explore/permalink":                         "superset.charts.write",
		"GET /api/v1/explore/":                                   "superset.charts.read",
		"GET /api/v1/chart/data/a-cache-key":                     "superset.charts.read",
		"POST /api/v1/chart/import/":                             "superset.charts.write",
		"DELETE /api/v1/chart/":                                  "superset.charts.delete",
		"POST /api/v1/tag/bulk_create":                           "superset.tags.write",
		"POST /api/v1/saved_query/import/":                       "superset.queries.write",
		"GET /api/v1/report/2/log/":                              "superset.reports.read",
		"GET /api/v1/query/updated_since":                        "superset.queries.read",
		"GET /api/v1/dataset/27/related_objects":                 "superset.datasets.read",
		"PUT /api/v1/dataset/warm_up_cache":                      "superset.datasets.write",
		"GET /api/v1/database/oauth2/":                           "superset.databases.read",
		"POST /api/v1/database/csv_metadata/":                    "superset.databases.write",
		"GET /api/v1/database/1/table_metadata/extra/":           "superset.databases.read",
		"GET /api/v1/database/1/select_star/people/public/":      "superset.databases.read",
		"GET /api/v1/database/1/table/people/public/":            "superset.databases.read",
		"GET /api/v1/dashboard/9/screenshot/abcdef/":             "superset.dashboards.read",
		"POST /api/v1/dashboard/9/favorites/":                    "superset.dashboards.write",
		"DELETE /api/v1/dashboard/9/favorites/":                  "superset.dashboards.delete",
		"POST /api/v1/dashboard/9/embedded":                      "superset.dashboards.write",
		"GET /api/v1/dashboard/9/embedded":                       "superset.dashboards.read",
		"GET /api/v1/dashboard/9/tabs":                           "superset.dashboards.read",
		"GET /api/v1/dashboard/9/datasets":                       "superset.dashboards.read",
		"GET /api/v1/dashboard/export/":                          "superset.dashboards.read",
		"DELETE /api/v1/dashboard/":                              "superset.dashboards.delete",
		"POST /api/v1/dashboard/":                                "superset.dashboards.write",
		"GET /api/v1/dashboard/_info":                            "superset.dashboards.read",
		"GET /api/v1/dashboard/related/owners":                   "superset.dashboards.read",
		"GET /api/v1/dashboard/favorite_status/":                 "superset.dashboards.read",
		"GET /api/v1/annotation_layer/1/annotation/2":            "superset.annotations.read",
		"PUT /api/v1/annotation_layer/1/annotation/2":            "superset.annotations.write",
		"DELETE /api/v1/annotation_layer/1/annotation/2":         "superset.annotations.delete",
	} {
		method, path, _ := cutRequest(request)
		assert.Equal(t, action, catalog.Match(method, "", path).ID, request)
	}

	// A request for a host is not Superset's: its routes name none.
	assert.Equal(t, "superset.unlisted", catalog.Match(http.MethodGet, "superset.example.com", "/api/v1/chart/1").ID)
}

func cutRequest(request string) (method, path string, ok bool) {
	for i := range request {
		if request[i] == ' ' {
			return request[:i], request[i+1:], true
		}
	}

	return "", "", false
}
