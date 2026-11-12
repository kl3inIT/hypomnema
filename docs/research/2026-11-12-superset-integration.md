# Superset as an Integration: reading a dashboard's charts as data

Date: 2026-11-12. Asked by the owner on 2026-11-12: figures of a Superset dashboard are to reach Lark through a
connector in Hypomnema, not a script outside it. This note holds what was tried and read for the first half,
[issue #203](https://github.com/kl3inIT/hypomnema/issues/203): Superset as an Integration that reads a dashboard's
charts as data. The second half, work on a schedule that writes them to Lark Base, has its own issue.

Read in the clones under `.tmp/`: `superset-mcp-winding` (Winding2020/superset-mcp, commit ff8e0eb, MIT per its
`package.json`), `superset-mcp-aptro` (aptro/superset-mcp, commit b3dd425, MIT), `superset-sup` (Preset's `sup`,
Business Source License 1.1), `preset-backend-sdk` ("All rights reserved"), `treg` (its README).

## 1. What was tried

### On a deployment in use, read-only, from the owner's signed-in browser (Superset 6.1.0, sign-in through Keycloak only)

| Tried | Result |
|---|---|
| `GET /api/v1/dashboard/9/charts` | 15 charts on 3 datasets, each with its stored settings (`form_data`) |
| `GET /api/v1/chart/{id}/data/` (the chart's saved query) | Works for 1 of the 4 table charts; the other 3 answer 400 "Chart has no query context saved. Please save the chart again." |
| `POST /api/v1/chart/data` with a query built from the chart's `form_data` | Works for all three: 160, 274 and 21 rows, the counts the dashboard shows |
| A bearer token not signed by Superset (RS256, as Keycloak's) | 422 "The specified alg value is not allowed". Superset's API takes only its own tokens |
| A request that is not a GET, without `X-CSRFToken` | 400 "The CSRF token is missing." |
| Its built-in MCP server (`/mcp`), Alerts and Reports, embedding | Off |

The kinds of chart on that dashboard: `table` (raw and grouped), `big_number_total`, `pie`,
`echarts_timeseries_line`, `echarts_timeseries_bar`.

### In a throwaway `apache/superset:6.1.0` on the owner's test host

| Tried | Result |
|---|---|
| `POST /api/v1/security/login` with `{"username", "password", "provider": "db", "refresh": true}` | 200 with `access_token` and `refresh_token`, for a local account, also where the pages sign in through Keycloak only. A wrong password: 401 `{"message": "Not authorized"}` |
| `POST /api/v1/security/refresh` with the refresh token as bearer | 200 with a new `access_token` |
| A request with no token | 401 `{"msg": "Missing Authorization Header"}` |
| A request with a token Superset cannot read | 422 `{"msg": "…"}`; Superset's own validation errors are under `message`, not `msg` |
| `GET /version` | 200 `{"build_number": null, "version_sha": "", "version_string": "6.1.0"}`, with or without a token |
| `GET /api/v1/security/csrf_token/` | 200 `{"result": "<token>"}` and a `session` cookie |
| A role holding Gamma's `can_read` permissions and `all_datasource_access` | Reads dashboards, charts and data; every write is 403 |
| `GET /api/v1/chart/{id}` of a chart of the examples | `datasource_id` is the chart's real dataset (3); the `datasource` inside its stored `params` named another (`23__table`), which no longer exists and answers 404. `GET /api/v1/dashboard/{id}/charts` gives the settings with the real one |
| `changed_on_utc` of a dashboard | `2026-11-12T09:35:31.504757+0000` |
| A raw table asked with `"metrics": []` | Superset groups by every column (`GROUP BY` on all of them), so rows that repeat are dropped; asked with no `metrics` at all, the rows come as they are |

### Hypomnema's own code against that throwaway Superset (2026-11-12, this issue's branch)

A control plane built from the branch, on a laptop, reached the container through an SSH port forward.

| Tried | Result |
|---|---|
| `hypomnema integrations connect superset --label probe --url … --user … --password-file …` | "connected as probe at …, version 6.1.0" |
| `hypomnema superset dashboards --search Sales`, `superset dashboard 7` | The two dashboards; ten charts with kind and dataset, seven filters |
| `superset chart` on charts of the examples: 4 `table` (grouped, with a percent metric, with a row limit, raw), 2 `big_number_total`, 2 `pie`, 2 `echarts_timeseries_line`, 3 `echarts_timeseries_bar` | Each ran: Superset accepted the built query and returned rows and its SQL. The raw table's SQL had no `GROUP BY` |
| `superset chart` on a `heatmap_v2` | "not read yet: heatmap_v2" |
| `superset api PUT /api/v1/chart/69` with a JSON body | Held; "Waiting for the owner's approval"; after `approvals approve`, the same command passed once and Superset answered 200 (the CSRF token, the cookie and the Referer were taken) |
| `superset api DELETE …` with the action set to never; a path with `..` | Refused before anything was sent |
| A coordinator token before and after `integrations chats superset:probe on` | Refused as not given; then read the dashboards |
| A read 17 minutes after the sign-in (the image's access token lasts 15) | Superset's log: `GET /api/v1/dashboard/` 401, `POST /api/v1/security/refresh` 200, `GET /api/v1/dashboard/` 200; the command printed the dashboards |

Not tried there: the dashboards of the examples have no filter with a default value, so the dashboard's filters at
their defaults were exercised only in tests. How long tokens last on the deployment in use is its own setting and
was not read.

## 2. What the references do

| Point | Reference | Read where |
|---|---|---|
| A Superset connector in an integration platform | None found: treg has none (searched its code); Composio, Nango, Pipedream, n8n, Activepieces show none (web search only) | `.tmp/treg` |
| Sign-in | Address, user name and password; `POST /api/v1/security/login`; sign in again when a call is refused with 401; a CSRF token with its session cookie and a Referer for calls that are not GET | `Winding2020/superset-mcp` `src/client/base-client.ts`; `aptro/superset-mcp` `main.py` |
| A list asked with a query | The query as JSON in `q` | `superset-mcp` `src/client/dashboard-client.ts`, `listDashboards` |
| A chart as its dashboard shows it | Read the dashboard's charts and filters and the chart's settings; a filter applies to a chart in its `chartsInScope`, or not in its `scope.excluded` | `superset-mcp` `dashboard-client.ts`, `getChartQueryContext` |
| A service that is an address plus a credential the server keeps and adds | treg's `endpoint` tool: `base_url` and credential bindings; "relays, never models", "injects auth server-side" | `.tmp/treg/README.md` |
| A short-lived token minted from a kept secret | Hypomnema's Lark app token | `internal/controlplane/integration/app.go` |
| A chart's data, in order: its saved query, the chart's data address, a query built from its settings | `sup chart data`. Business Source License 1.1: its behaviour is learned, its code is not copied, and it is not shipped | `.tmp/superset-sup/src/sup/clients/superset.py` L478-590 |
| The commands a Superset client has | `sup`: sql, workspace, database, dataset, chart (list, sql, data, pull, push), dashboard, query, user, role, sync | `.tmp/superset-sup/README.md` |
| An official client | Apache's `superset` command manages a server from inside it. Preset's `preset-cli` signs in to a self-hosted Superset by user name and password; "All rights reserved", and `sup` calls it legacy | `.tmp/preset-backend-sdk` |
| Every action of a service with an answer the owner sets | Hypomnema's service proxy: a catalog of actions, each allow, ask or never for a Connection; Lark's catalog is taken from lark-cli's own list of methods | `internal/controlplane/serviceproxy` |
| Superset's API, method by method | `superset.spec.json`, Superset's own OpenAPI description: 157 paths, 210 methods, the same bytes in both MCP servers | `.tmp/superset-mcp-winding/superset.spec.json` |

## 3. What was built from it

What exists is in [ARCHITECTURE.md](../../ARCHITECTURE.md) (Integrations, Superset). Where each rule comes from:

| Rule | From |
|---|---|
| A third kind of Integration, `account`: an address, a user name and a password; the address is the Connection's | Both Superset MCP servers; treg (`base_url` on the tool) |
| Connecting signs in once and stores nothing when refused | Hypomnema's Lark Connection |
| Tokens in memory; refresh, then sign in again, when a request is refused for its token | Hypomnema's Lark app token; `superset-mcp` `base-client.ts`; the probes above (401, and 422 with `msg`) |
| The catalog: Superset's own methods, a read, a change and a delete a part, `POST /api/v1/chart/data` a read, running SQL an action of its own | Hypomnema's Lark catalog; the owner, 2026-11-12 ("configurable, as mail is") |
| A request relayed with the token added on the server | treg (`treg call`) |
| A chart's data: the saved query first, a built query after | `sup` (the order) |
| The dashboard's filters at their defaults, for the charts in their scope | `superset-mcp` (`getChartQueryContext`) |
| The query of each kind of chart | Superset's own chart plugins (see below) |
| A chart's dataset is the one its record names, not the one in its stored settings | The probe above |

The query of each kind follows Superset's own front end, version 6.1.0. Its source is not among the clones: the
rules below were written from knowledge of `superset-frontend` (`buildQueryObject`, the table, Big Number, Pie and
Timeseries plugins' `buildQuery`), and each kind was then run against Superset 6.1.0 (section 1). They are
inferences from that source until someone reads it in a clone.

| Kind | The query |
|---|---|
| Every kind | The chart's simple filters as column, operator, value; its SQL filters into `extras.where` or `extras.having`; `row_limit`, `time_range`, the time column and the time grain when the chart has them. A filter on a time column with the range "No filter" is left out (tried on the deployment in use) |
| `table`, raw (the mode is `raw`, or the chart names columns and no mode) | `columns` are `all_columns`; no `metrics`; `orderby` from `order_by_cols` |
| `table`, grouped | `columns` are `groupby`, a time column cut to the time grain; `metrics` are `metrics` and `percent_metrics`; ordered by `timeseries_limit_metric` (the direction from `order_desc`) or by the first metric, the largest first; the percent metrics as a `contribution` |
| `big_number_total` | The one `metric`, no columns |
| `pie` | `groupby` and the one `metric`; ordered by the metric when `sort_by_metric` is set |
| `echarts_timeseries_line`, `echarts_timeseries_bar` | The x axis as a column cut to the time grain, then `groupby`; `series_columns` are `groupby`; `metrics`; ordered by the series' limit metric or the first metric; the series limit. The rows are the query's own, one for each x and series: the chart's pivot is not applied |
| The dashboard's filters | For each filter that applies to the chart, its `defaultDataMask.extraFormData`: `filters` and `adhoc_filters` are added; `time_range`, `granularity_sqla` and `time_grain_sqla` replace the query's own, and the time range also replaces the range of the query's filters on a time column |

The first probe's script ordered a grouped table by `[[metric, !order_desc]]` in both cases; what was built orders
by the first metric the largest first when the chart names no sort metric, which is the table plugin's rule. The
row counts do not depend on the order.

## 4. Left for later

- A saved query is sent as Superset saved it, with the dashboard's filters added; a chart whose settings changed
  after its query was saved is read by the saved one.
- A chart of another kind is refused by name. Each kind added needs its plugin read.
- A chart's data under chosen filter values, a dataset's columns and metrics as a command, pictures of charts:
  through `superset api` until built.
