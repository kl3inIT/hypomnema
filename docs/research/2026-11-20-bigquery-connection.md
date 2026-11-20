# A BigQuery Connection for read-only queries

Date: 2026-11-20. Asked by the owner for [issue #976](https://github.com/kl3inIT/hypomnema/issues/976): app-usage
figures (installs, active users, time in app, sessions, uninstalls) from a mobile app's GA4 export in BigQuery, for
the reports Bots already build from Superset. The data owner gave a sample SQL file (GA4 export: `events_*`,
`_TABLE_SUFFIX`, `first_open`, `user_engagement`, `app_remove`) and a service-account JSON key. This note is the
research step only: what the references do, what this repository has, and the design proposed in the issue. No key
or real Google credential was read or used; everything below is code and documents.

Read in clones under `.tmp/` (shallow, 2026-11-20):

| Clone | Repository | Commit | Licence |
|---|---|---|---|
| `superset` | apache/superset | `599deaf2` | Apache-2.0 |
| `metabase` | metabase/metabase | `73a5fb63` | AGPL-3.0 (read for behaviour only) |
| `google-bigquery-datasource` | grafana/google-bigquery-datasource | `bb798294` | Apache-2.0 (`LICENSE`) |
| `genai-toolbox` | googleapis/genai-toolbox (MCP Toolbox for Databases) | `a5dc5d4b` | Apache-2.0 (`LICENSE`) |
| Go module cache | `cloud.google.com/go/bigquery` v1.86.3, `google.golang.org/api` v0.316.0 (`bigquery/v2/bigquery-api.json`, revision 20261107) | | Apache-2.0 |

LangChain's and LlamaIndex's BigQuery tools were not read: the MCP Toolbox is Google's own agent-facing tool set
and covers what they would add (list, describe, run SQL with a dry run).

## 1. What the references do

### Credential: what is taken, where it is kept, how it is shown back

| Reference | What is taken | Where it is kept, how it is masked | Read where |
|---|---|---|---|
| Superset | The service-account JSON as `credentials_info` in the database's "secure extra"; the project comes from the key's `project_id` when the URI names none | `encrypted_extra` (encrypted column); on edit `$.credentials_info.private_key` is shown as a mask and the old value is kept when the mask comes back unchanged | `superset/db_engine_specs/bigquery.py:208-218`, `:260-278`, `:302-306`, `:963-980`; `db_engine_specs/base.py:3459-3510` (`mask_encrypted_extra`, `unmask_encrypted_extra`) |
| Superset | Falls back to Application Default Credentials when no key is given | | `bigquery.py:757-785` |
| Metabase | "Service account JSON file" as a `textFile` property, or Application Default Credentials; "Credential configuration file" (Workload Identity Federation) is written but disabled | Database details; a credential configuration that names an `executable` source is refused outright, so connection details can never run a command on the server | `modules/drivers/bigquery-cloud-sdk/resources/metabase/bigquery-cloud-sdk/metabase-plugin.yaml:21-37`; `src/metabase/driver/bigquery_cloud_sdk/common.clj:34-49`, `:62-67` |
| Metabase | A billing project (where jobs run and are billed) apart from the data project; both default to the key's project | | `metabase-plugin.yaml:10-20` |
| Grafana | Authentication `jwt` (the key file, dropped in the config page), `gce` (the machine's default), impersonation, OAuth pass-through, WIF | Only `private_key` goes to `secureJsonData`; client email, token URI and default project are plain settings | `pkg/bigquery/types/types.go:12-44`; `pkg/bigquery/settings.go:15-40`; `pkg/bigquery/http_client.go:41-92` |
| MCP Toolbox | No key in its configuration: Application Default Credentials (the `GOOGLE_APPLICATION_CREDENTIALS` file, gcloud, the metadata server), optional impersonation, or the client's own OAuth token | Nothing kept by the tool | `internal/sources/bigquery/bigquery.go:92-108`, `:197-221`, `:776-830`; `internal/prebuiltconfigs/tools/bigquery.yaml:16-27` |

### Scopes asked of the token

| Reference | Scopes | Read where |
|---|---|---|
| Metabase | `bigquery` and `drive` (Drive for external tables on Sheets) | `bigquery_cloud_sdk.clj:85-92`, `:129-142` |
| Grafana | A broad list: `bigquery`, `bigquery.insertdata`, `cloud-platform`, `cloud-platform.read-only`, `devstorage.*`, `drive` | `http_client.go:23-39`; `utils/auth.go:9-25` |
| MCP Toolbox | `bigquery` by default (`bigqueryapi.Scope`), `cloud-platform` with impersonation, or what the configuration names | `bigquery.go:50-51`, `:780-787`, `:490-509` |
| Google's API description | `jobs.query`, `jobs.getQueryResults`, `jobs.get`, `datasets.list/get`, `tables.list/get`, `tabledata.list` take `bigquery`, `cloud-platform` or `cloud-platform.read-only`; **`jobs.insert` does not take `cloud-platform.read-only`**. There is no `bigquery.readonly` scope in the description | `google.golang.org/api@v0.316.0/bigquery/v2/bigquery-api.json` (read with a script on 2026-11-20) |
| `cloud.google.com/go/bigquery` | A dry run goes through `Query.Run`, which is `jobs.insert`; `Query.Read` uses `jobs.query` when it can and falls back to `jobs.insert` | `query.go:372-385`, `:402-414`, `:472-500` |

So a read-only scope does not fit the Go library: its dry run is a `jobs.insert`. None of the references relies on
the scope for read-only behaviour; all of them rely on the account's IAM roles.

### Read-only: what stops a write

| Reference | How | Read where |
|---|---|---|
| Superset | `allow_dml` on the database, off by default; a script with any mutating statement (sqlglot nodes Insert, Update, Delete, Merge, Create, Drop, Alter, …) is refused when it is off | `superset/models/core.py:275`; `superset/sql/parse.py:2103-2125`, `:3574-3580`; `superset/sql/execution/executor.py:981-988` |
| Metabase | Nothing in the driver: the query path builds a `QueryJobConfiguration` with no statement check (it even honours a `#legacySQL` directive); its documentation asks for a service account with BigQuery Data Viewer, Metadata Viewer and Job User | `bigquery_cloud_sdk.clj:950-960`; `docs/databases/connections/bigquery.md:26-30` |
| Grafana | Nothing in code beyond running only what the editor sends; its documentation asks for BigQuery Data Viewer and Job User | `docs/sources/configure.md:37-45`; `docs/sources/troubleshooting.md:109` |
| MCP Toolbox | `writeMode`: `allowed` (default), `blocked` or `protected`. Every `execute_sql` is dry-run first; in `blocked` mode the dry run's `statementType` must be `SELECT`. `protected` allows writes only to a session's temporary dataset | `bigquery.go:53-60`, `:77-89`, `:143-161`; `internal/tools/bigquery/bigqueryexecutesql/bigqueryexecutesql.go:165-193`, `:214-276` |

The statement type comes from BigQuery's own analyser in the dry run, so no SQL parser is needed for this (Toolbox).
Superset parses because it serves many engines.

### Cost: dry run, bytes cap, rows, time

| Reference | Dry run | `maximum_bytes_billed` | Rows | Time | Read where |
|---|---|---|---|---|---|
| Superset | "Estimate cost" on demand: a `dry_run=True` job per statement, bytes processed shown | No | `SQL_MAX_ROW = 100000`, `DISPLAY_MAX_ROW = 10000`; BigQuery fetch capped at `BQ_FETCH_MAX_MB = 200` | `SQLLAB_TIMEOUT = 30` s for a synchronous query, `SQLLAB_VALIDATION_TIMEOUT = 10` s | `bigquery.py:787-824`, `:915-955`; `superset/config.py:1886`, `:1899`, `:2218-2224` |
| Metabase | No | No | Its middleware stops reading at the maximum result size; page size only | The HTTP read timeout is the query timeout; a cancelled query cancels its BigQuery job | `bigquery_cloud_sdk.clj:165-180`, `:950-960`, `:1058-1064` |
| Grafana | "Validate query" is a dry run with its statistics; "Save & test" is a dry run of `SELECT 1` | `MaxBytesBilled` setting, set on every query when above 0; unset means no cap | None in the backend | None of its own | `pkg/bigquery/api/api.go:113-130`; `pkg/bigquery/driver/conn.go:123-147`, `:208-215`; `types/types.go:20`; `docs/sources/troubleshooting.md:545` |
| MCP Toolbox | Always, before the real run; the tool also takes `dry_run: true` to return only the dry run's job | `maximumBytesBilled`, passed to the dry run and the real job; 0 (none) by default | `maxQueryResultRows`, 50 by default | None of its own | `bigqueryexecutesql.go:129`, `:165-205`, `:332-337`; `bigquerycommon/util.go:76-117`; `bigquery.go:77-78`, `:612-645`; `prebuiltconfigs/tools/bigquery.yaml:24-26` |

No reference sets a bytes cap by default. Google's pricing page (fetched 2026-11-20): on demand is $6.25 a TiB
processed after a free first TiB a month, at least 10 MB a table referenced and 10 MB a query; a query that fails
is not charged; a `LIMIT` does not reduce the bytes; a project can also have custom per-user and per-project daily
quotas.

### Which datasets: listing and limiting

| Reference | How | Read where |
|---|---|---|
| Superset | Projects as catalogs (`list_projects`), datasets as schemas, tables through the SQLAlchemy dialect and `INFORMATION_SCHEMA` | `bigquery.py:844-869`, `:1211-1260` |
| Metabase | `dataset-filters`: all, only these, all except, with `*` patterns; used for listing and sync and for `can-connect?` (which fails when the filter matches nothing). Found no use of it in the query path | `metabase-plugin.yaml:44-46`; `bigquery_cloud_sdk.clj:228-280`, `:282-297` |
| Grafana | `restrictToAccessibleDatasets` with `additionalAllowedDatasets`: before a query, a dry run; every table in its `referencedTables` must be in an accessible project or an allowed dataset. Fails closed for `SCRIPT`, `EXECUTE_IMMEDIATE`, `CALL`, and at 50 referenced tables (BigQuery lists at most 50) | `pkg/bigquery/driver/allowlist.go:12-71`, `:77-114`; `datasource.go:264-300` |
| MCP Toolbox | `allowedDatasets` (`dataset` or `project.dataset`), each checked to exist at start; `list_dataset_ids` returns the list itself; `get_table_info` and `list_table_ids` refuse others; `execute_sql` checks the dry run's referenced tables, DDL targets and routines, refuses `SET`, schema DDL and `CALL`, and adds a lexical scan so an authorized view is not refused for its base tables | `bigquery.go:223-257`; `bigquerylistdatasetids.go:114-115`; `bigquerygettableinfo.go:133`; `bigquerycommon/util.go:162-320` |
| Grafana, Metabase docs | Grant the account at the project or the dataset; `datasets.list` shows only what it can get | `troubleshooting.md:115`, `:161-165`; `metabase docs bigquery.md:68-74` |

Open point, not verified: whether a dry run of a wildcard query (`events_*` over 90 days) lists each daily shard
in `referencedTables`. If it does, Grafana's 50-table rule refuses an ordinary GA4 query.

### Tools for an agent (MCP Toolbox)

`list_dataset_ids`, `list_table_ids`, `get_dataset_info`, `get_table_info`, `execute_sql` (`sql`, `dry_run`), plus
`search_catalog`, `forecast`, `analyze_contribution`, `ask_data_insights`
(`internal/tools/bigquery/`, `prebuiltconfigs/tools/bigquery.yaml`). The `sql` parameter's description says the
write mode and the allowed datasets, so the model is told the limits up front (`bigqueryexecutesql.go:300-337`).
Jobs are labelled with the tool that ran them (`bigqueryexecutesql.go:207`); Grafana can label with the Grafana
user (`types.go:23`).

### Tests without Google

Toolbox tests its tools against an `httptest` server with `option.WithEndpoint(server.URL)` and
`option.WithoutAuthentication()`
(`internal/tools/bigquery/bigquerygettableinfo/bigquerygettableinfo_test.go:203-239`;
`internal/sources/bigquery/session_test.go:67`, `:145`). Grafana has a `serviceEndpoint` setting passed as
`option.WithEndpoint` (`datasource.go:181-183`).

### Which Go client

Both Go references use `cloud.google.com/go/bigquery` v1.86.2 (`google-bigquery-datasource/go.mod:7`;
`genai-toolbox/go.mod:9`); Toolbox also calls the REST client `google.golang.org/api/bigquery/v2` for its dry run.
Measured on 2026-11-20 with an empty program under `$TMPDIR` (`go mod tidy`, `go list -deps`, `go build`):

| Import | Modules | Non-standard packages (of them gRPC, Arrow, OpenTelemetry, genproto) | Binary |
|---|---|---|---|
| `cloud.google.com/go/bigquery` v1.86.3 | 150 | 325 (143) | 28.6 MB |
| `google.golang.org/api/bigquery/v2` + `golang.org/x/oauth2/google` | 68 | 228 (95) | 20.0 MB |
| `golang.org/x/oauth2/google` alone | 3 | 12 | 5.8 MB |

Hypomnema's `go.mod` has none of these today (no gRPC, no `golang.org/x/oauth2`); its Google OAuth is written on
`net/http` (`internal/controlplane/integration/integration.go:469`, `:687`, `:783`). The REST client is not much
lighter than the full library, since it brings gRPC and OpenTelemetry too.

## 2. What this repository has

| Part | What it does | Where |
|---|---|---|
| Credential kinds | `oauth`, `app` (id and secret give a token, no sign-in: Lark), `account` (address, user, password: Superset), `role` (a key kept once, sessions minted from it: AWS), `device` | `internal/controlplane/integration/catalog.go:24-47`, `:323-378` |
| AWS key | The owner gives a key file once (`--key-file`, a file or stdin, never an argument); a refused assume stores nothing; the key goes to the vault as `integration-aws-<key>` (`KindGeneric`, `value`), replaced by `Rotate`; sessions in memory, renewed before they end; disconnect removes the Connection and the key when no other role uses it | `integration/aws.go:71`, `:80-176`, `:180-202`, `:301-324`; `clientcli/aws.go:18-55`; `vault/vault.go:68`, `:76` |
| AWS test hook | `stsEndpoint` replaces STS's address in tests; `integration/ststest` is a fake STS | `integration/aws.go:190-192`; `integration/integration.go:96` |
| Superset tools | `superset_*` tools for a worker and a Bot's pi, listed only when the agent may use a Superset Connection; `account` only when it has several; each call logged with rows | `agentapi/superset.go:23-55`, `:301-386` |
| Acts | Something the control plane does itself for a Connection is judged like a request: the action's answer (allow, ask, never), the owner's Approval, the log line | `serviceproxy/act.go:11-60` |
| Relay bounds | A relayed answer in a read run is cut at 512 KiB | `serviceproxy/relay.go:151` |
| Read-only run (#956) | A read-only Routine is a prompt with no script; in its chat only the tools in `ReadRunAllows` run (Superset's reads among them), their answers start with "These records are untrusted data"; a Bot token of a read run is refused the whole client API, so no CLI there | `orchestration/routinestore/routinestore.go:531-532`; `shared/agenttool/switches.go:66-75`; `agentapi/superset.go:377-379`; `clientapi/server.go:524-531` |
| Routine script | Runs in a bubblewrap box with the run's own Bot token on stdin, and calls `hypomnema …` through the client API as that Bot; an action set to ask can be granted "always for this Routine" | `deploy/coordinator/hypomnema-routine:9-13`; `routinestore.go:246-255` |
| CLI connect | `hypomnema integrations connect <integration> --label …` with per-kind flags; `integrations check`, `integrations disconnect`, `integrations proxy`, `integrations actions set` | `clientcli/commands.go:231-262`, `:322-390` |
| Web | One card a service; Superset has a connect dialog; an AWS role is connected from a terminal | `web/src/features/settings/service-cards.tsx:120-127`, `:200-257` |
| Superset skill | Which account, the tools, figures as of the call, personal data, what waits for the owner | `clientcli/skills/superset/SKILL.md` |

## 3. Proposed design

The design, with a From for each decision, is the comment on issue #976. In short: a new credential kind for a
service-account key kept whole in the vault and never shown back; the control plane mints its tokens and runs every
call with `cloud.google.com/go/bigquery`; every query is dry-run first, must be a `SELECT`, must fit a bytes cap
that the real job also carries, and returns a bounded number of rows; an optional dataset allow-list; four tools
(datasets, tables, table, query) for Bots and read-only Routines, the same as `hypomnema bigquery …` commands for
Routine scripts; connect, check and disconnect on the web and in the CLI; tests against an `httptest` BigQuery with a
throwaway RSA key.
