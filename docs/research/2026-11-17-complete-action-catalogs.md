# Complete action catalogs for GitHub, GitLab, Google and Lark (2026-11-17)

For [issue #713](https://github.com/kl3inIT/hypomnema/issues/713): a Bot runs a service's own CLI (`gh`, `glab`,
`gws`, `lark-cli`) through the service proxy, and a request that no action of the Integration's catalog names asks as
`<integration>.unlisted`, whatever the owner set. How should each catalog be made complete, from what declaration,
grouped how, kept current with the CLI, and with merge and auto-merge as actions of their own (#714)? Absorbs #705
(GitLab for a Bot). Builds on [connectors](2026-11-10-connectors.md) and
[the service proxy decision](../decisions/2026-11-11-service-proxy-in-the-control-plane.md).

Clones are in `.tmp/`; the declarations were downloaded into `.tmp/specs/`. Everything below was read in code or in
the downloaded files; a line marked *inference* was not.

## What was read

| Source | Revision |
|---|---|
| Hypomnema `main` | `1c2eef1` |
| `github/rest-api-description`, `descriptions/api.github.com/api.github.com.json` | `35829a5` (OpenAPI 3.0.3, description 1.1.4; SHA-256 `ab00ead2266c…`) |
| GitHub's public GraphQL schema, `https://docs.github.com/public/fpt/schema.docs.graphql` | fetched 2026-11-17 (SHA-256 `d928ebbb2d08…`); `octokit/graphql-schema` `82ff2d4` (2026-07-21) is older and lacks 5 mutations gh sends |
| `github/github-mcp-server` | `67d4dc4` (2026-11-16) |
| `cli/cli` (gh) | `0536d5e` (trunk, 2026-11-16); Hypomnema pins gh 2.103.3, the latest release is 2.104.0 (2026-11-12) |
| `pingdotgg/t3code` | `42a5aad` (2026-11-17) |
| `onyx-dot-app/onyx` | `cce0267` (2026-11-17) |
| GitLab's `doc/api/openapi/openapi_v2.yaml` (`gitlab-org/gitlab`) | `2d0550f` (Swagger 2.0; SHA-256 `96b92dd15a4e…`) |
| `gitlab-org/cli` (glab) | `206ebab` (trunk, 2026-11-17); latest release v1.130.0 (2026-11-10) |
| `gitlab-org/api/client-go` (the Go client glab is built on, `go.mod:52` pins v3.52.0) | `6e7adda` (2026-11-17) |
| `zereight/gitlab-mcp` (2.3.28) | `f293f81` (2026-11-17) |
| `googleworkspace/cli` (gws) | `cd3697e`, version 0.22.5, also the latest release (2026-03-31) |
| Google's Discovery documents of the 18 APIs gws names | fetched 2026-11-17 from `www.googleapis.com/discovery/v1/apis/<api>/<version>/rest` or `<api>.googleapis.com/$discovery/rest`, as gws does (`crates/google-workspace/src/discovery.rs:220-240`) |
| `taylorwilsdon/google_workspace_mcp` | `43b662a` (2026-11-14) |
| `larksuite/cli` (lark-cli) | `3d82c85` (main); the latest release v1.0.100 is `ac22a60` (2026-11-10), the commit `lark.go` was taken from |

## Hypomnema today (checked on `main` 1c2eef1)

- **The unlisted action** always asks: `catalog.go:70-78`. A request is matched by method, host and path; of the
  routes it matches, the one naming the most literal segments wins, the first between equals (`catalog.go:117-138`).
- **GitHub** (`github.go:15-28`, `57-160`): Onyx's list plus reads; 18 actions, 5 writes (issue, comment, pull
  request, file, ref), GraphQL matched by operation type and root field, the strictest action governing a request
  (`github.go:185-233`). The introspection reads of #706 are on `main` as `github.schema.read` (`github.go:111-115`).
  The issue's citation `github.go:13-23` is now `15-28`. No merge, auto-merge, label, close, rerun or release write.
- **Google** (`catalog.go:279-427`): 19 hand-written actions (18 listed and discovery as plumbing) on Gmail,
  Calendar, Drive, Docs, Sheets and Search Console, read off one probe of gws 0.22.5 and Onyx. As the issue says.
- **Lark** (`lark.go:12-36`, `89-162`): `lark_methods.json`, 440 methods of lark-cli 1.0.100 with lark-cli's own risk,
  grouped as service × {read, write, risky}; a method it does not name falls to its service's part of the API by
  HTTP method (`lark.go:108-125`), and any other GET is `lark.other.read` (`lark.go:153-159`). **The extractor that
  made the file is not in the repository** (searched for `lark_methods`: only `lark.go` and `ARCHITECTURE.md`); #236
  describes it but did not commit it.
- **The issue's three Lark unlisted approvals** (`POST /base/v3/bases/{id}/tables`, `POST /docs_ai/v1/documents`):
  both are named on `main` now. `lark_methods.json` has `POST /open-apis/base/v3/bases/{created_base_token}/tables`
  as `base` write since #236 (2026-11-12), and `POST /open-apis/docs_ai/...` falls to `doc` write. *Inference:* the
  approvals on zm predate #236; zm's table was not read from this node (no client access).
- **Superset** (`superset.go:11-19`, `55-70`): the API's own description, three fields kept per method, grouped by
  resource × {read, write, delete}; the few POSTs that only read are listed one by one with the description's own
  sentence. This is the shape every catalog below takes.
- **The owner's answers are keyed by action id**: `action_policies (integration, label, action)`
  (`orchestration/migrations/00042_approvals.sql:34-41`) and `routine_grants (… action …)`
  (`00056_routines.sql:78-88`). An id that stays the same keeps its answer; an id that disappears loses it.
- **Generated code is checked** by regenerating and diffing in `make verify` (`Makefile:53-67`, sqlc and oapi-codegen,
  "as Omnara's openapi-check"). No `go:generate` is used in the repository.
- **Tool versions** are pinned with checksums: gws 0.22.5, lark-cli 1.0.100, gh 2.103.3
  (`internal/shared/agenttool/catalog.go:153, 193, 286`). There is no glab.

## 1. What is a complete declaration, per service

| Service | Declaration | Size | Complete? |
|---|---|---|---|
| GitHub REST | `rest-api-description`: method, path, `operationId`, `x-github.category` and `subcategory` per operation | 1281 operations (667 GET, 200 POST, 141 PUT, 79 PATCH, 194 DELETE), 51 categories | Yes for `api.github.com`; release asset uploads are on `uploads.github.com` (the operation's own `servers`) |
| GitHub GraphQL | The public schema: `type Query` (31 root fields, line 48918) and `type Mutation` (285 fields, line 27586), each mutation with `@docsCategory(name: …)` in the same vocabulary as REST's categories (`mergePullRequest` → `pulls`, `createIssue` → `issues`) | 269 of 285 mutations carry a category | Yes. Every mutation gh's source names (46, from its `graphql:"<field>(input…"` tags) is in it; 5 of them (`addBlockedBy`, `replaceActorsForAssignable`, `requestReviewsByLogin`, …) are missing from octokit's older copy, so the docs schema is the one to read |
| GitLab REST | GitLab's `openapi_v2.yaml` | 1147 operations, 108 tags | **No.** It has no notes, discussions, labels or `/user` (checked: `/api/v4/projects/{id}/merge_requests/{merge_request_iid}/notes`, `/issues/{issue_iid}/notes`, `/projects/{id}/labels`, `/user` are absent) — what glab sends to comment is missing |
| GitLab, as glab calls it | client-go, the client glab is built on: every route is registered with `route("…")` so that "Routes and MatchRoute describe the whole client, not just the calls made so far" (`route.go:16-62`), and each call names its method beside it (`withMethod(http.MethodPut), withPath(routeProjectsIDMergeRequestsIDMerge, …)`, `merge_requests.go:956-963`) | 717 routes; 690 method + route pairs read by a regex over `withMethod/withPath`, the rest built another way (7 `NewRequest` calls and others) | Complete for what glab can send, by construction |
| GitLab GraphQL | glab sends only queries (work items: `internal/commands/workitems/api/api.go:127, 152`; no `mutation` outside tests) | | A query is a read, as for GitHub |
| Google | Discovery: each method's `httpMethod`, `flatPath` (`path` uses `{+name}` reserved expansion), `scopes`, and upload paths under `mediaUpload.protocols` (Drive's `files.create`: `/upload/drive/v3/files`, `/resumable/upload/drive/v3/files`); gws reads the same fields (`discovery.rs:77-87`) | 501 methods on the 18 APIs gws names (`services.rs:30-…`, 19 entries, one of them gws's own `workflow`) | Yes for what gws can call by name; gws can also call any other API as `<api>:<version>` |
| Lark | lark-cli's service registry (`internal/registry/catalog/services/*.json`, 15) and its shortcuts' dry runs (`shortcuts/*/`), as #236 did | 440 methods kept | Yes for v1.0.100, still the latest release; `main` is 2 commits ahead of it |

**Read versus write.** GitHub's MCP server marks every tool `ReadOnlyHint` (71 true, 62 false over
`pkg/github/*.go`; `pkg/inventory/server_tool.go:172-175`), zereight's GitLab server lets its raw-request tool send
only GET in read-only mode (`tools/gitlab-api-request.ts:108-116`), Onyx gives reads `ALWAYS` and an action without
a default `ASK` (`providers/actions.py:122`), and Superset's catalog makes a GET a read (`superset.go`). All four agree:
**a GET or a GraphQL query is a read, everything else is not**, with a short named list of POSTs that only read.

**Scopes are not a read signal.** Among Google's 501 methods, 27 that are not GET accept a `.readonly` scope, and some
of them change things: `drive.files.copy` and `slides.presentations.batchUpdate` both accept a read-only scope. So
the read-POSTs are named one by one, as `supersetReadPosts` does (`superset.go:55-63`), not inferred from scopes. The
Google POSTs that only read, by their Discovery description: `calendar.freebusy.query`,
`webmasters.searchanalytics.query`, `searchconsole.urlInspection.index.inspect` (the last two are already reads,
`catalog.go:419-425`), `chat.spaces.messages.search`, `drive.files.download`.

## 2. Grouping into actions the owner understands

| Source | Unit the owner (or user) chooses |
|---|---|
| github-mcp-server | Toolsets: context, repos, git, issues, pull_requests, users, orgs, actions, code_security, secret_protection, dependabot, notifications, discussions, gists, security_advisories, projects, labels, copilot… (`pkg/github/tools.go:23-190`), each tool read or not |
| zereight/gitlab-mcp | Three sets over 327 tools: `readOnlyTools` (150, `tools/registry.ts:1833`), `destructiveTools` (`:2007`, includes `merge_merge_request` at `:2051`), `deleteTools` (`:2068`) |
| google_workspace_mcp | Per service a ladder of levels: Gmail `readonly`, `organize`, `drafts`, `send`, `full`; others `readonly`, `full` (`auth/permissions.py:69-76`) |
| t3code | Per change request a fixed set of actions: merge, ready, draft, close, reopen, delete-branch, update-branch, enable-auto-merge, disable-auto-merge, revert (GitHub `packages/source-control-github/src/shared/definition.ts:116-128`; GitLab `packages/source-control-gitlab/src/shared/definition.ts:60-71`) |
| Onyx | A short hand list per provider, 13 to 15 actions (GitHub `providers/github.py`, Gmail `gmail.py`, Drive `google_drive.py`, Calendar `google_calendar.py`) |
| Hypomnema Lark, Superset | Service × {read, write, risky or delete}, from the declaration's own groups (`lark.go:129-151`, `superset.go:28-50`) |

They agree on a two-level shape: **a service, then what kind of thing is done in it**; and they carve out by name the
few actions that matter on their own (Gmail's send, t3code's merge and auto-merge, zereight's merge in the destructive
set). The GitHub categories fold into 13 services with every REST category and 269 mutations placed (counts:
read/write/delete REST, then mutations): Repositories 110/64/30, 37; Issues 35/28/18, 54; Pull requests 16/19/4, 36;
Actions and checks 109/80/31, 5; Releases 6/5/2; Search 7; Projects 14/10/2, 32; Discussions 0, 13; Gists 11/6/3;
Security 63/44/11, 1; Organizations and teams 116/68/40, 72; Account 119/56/35, 13; Copilot and agents 61/40/18, 5;
and 16 mutations with no category. About 38 actions, against Lark's 57 today.

## 3. Keeping curated names and the owner's answers

The answers are keyed by id (above), so the curated ids must survive generation unchanged. Superset's catalog shows
the way: the declaration gives every method, and a short table in Go names the exceptions by their method and path
(`supersetReadPosts`, `supersetSQL`, `superset.go:55-70`). Onyx names actions by hand and has no generated list.
github-mcp-server names tools by hand too. No reference regenerates a catalog over stored answers; the closest is
Hypomnema's own Lark list, regenerated in #236 and #567 with the same `lark.<service>.<risk>` ids.

So: the declaration gives every operation; an **overlay table** maps chosen operations (by `operationId`, GraphQL
field, Discovery method id, or client-go route) to the curated ids (`github.pulls.create`, `gmail.send`, …); every
other operation takes its group's generated id (`github.<group>.read|write|delete`). The existing tests' requests
(`github_test.go:33-…`, `proxy_test.go`) must still name the same ids: that is the check that no answer is lost.

The specificity rule of `Match` (most literal segments win) makes this work without ordering tricks, because each
declared operation's template is exact; a hand route like `repo+"/pulls/{rest...}"` would otherwise lose to a
declared `/repos/{owner}/{repo}/pulls/{pull_number}/files`. A trailing parameter that may hold slashes (GitHub's
`path`, `ref`, `branch`, `basehead`; Google's `{+name}`) becomes `{name...}`, as the hand routes already do
(`github.go:118-128`).

## 4. Merge and auto-merge

| Source | Merge | Auto-merge |
|---|---|---|
| gh | GraphQL `mergePullRequest` (`pkg/cmd/pr/merge/http.go:101-103`) | `enablePullRequestAutoMerge`, `disablePullRequestAutoMerge` (`http.go:90-95, 108-110`) |
| GitHub REST | `PUT /repos/{owner}/{repo}/pulls/{pull_number}/merge`, `PUT …/merge-async` | None in REST (the `agent-merge` operations are a different thing) |
| github-mcp-server | `merge_pull_request`, `ReadOnlyHint: false` (`pkg/github/pullrequests.go:1711-1718`) | No tool |
| glab | `PUT /projects/:id/merge_requests/:iid/merge` | **The same request** with `auto_merge: true`; glab sets it by default when the MR has a pipeline (`--auto-merge` defaults to true, `internal/commands/mr/merge/mr_merge.go:93, 223-229`) |
| client-go | `AcceptMergeRequest` (`merge_requests.go:956-963`) | `auto_merge` and the deprecated `merge_when_pipeline_succeeds` options (`merge_requests.go:945, 953`); cancel by `POST …/cancel_auto_merge` or `…/cancel_merge_when_pipeline_succeeds` (`merge_requests.go:33-34`) |
| t3code | `merge` | `enable-auto-merge`, `disable-auto-merge` as actions of their own; on GitLab `glab mr merge --auto-merge=true` and cancel by `glab api …/cancel_merge_when_pipeline_succeeds` (`GitLabPullRequestCli.ts:1009-1027, 2798-2806`) |
| zereight/gitlab-mcp | `merge_merge_request`, destructive (`tools/registry.ts:2051`) | `auto_merge` and `merge_when_pipeline_succeeds` options of the same tool (`schemas.ts:2624-2629`) |
| Onyx | none: a merge asks as unspecified (`matching/engine.py:151-158`) | none |

t3code is the only reference that makes merge and auto-merge separate actions, and it is what #714 needs: the owner
allows auto-merge (the host merges once checks pass) without allowing an immediate merge. On GitLab the two are one
route, told apart by the body or query (`auto_merge` or `merge_when_pipeline_succeeds` true); because glab defaults to
auto-merge, a plain `glab mr merge` on a merge request with a pipeline is an auto-merge.

## 5. A check that the catalog has not fallen behind the CLI

| Source | How a generated or derived list is kept current |
|---|---|
| github-mcp-server | `generate-docs` regenerates README from the tools; CI fails on a diff (`.github/workflows/docs-check.yml`) |
| t3code | Vendors GitHub's public schema (`schema/github.graphql`, from `docs.github.com/public/fpt/schema.docs.graphql`, `graphql.config.ts:3`) and generates typed operations from it (`pullRequestMutations.graphql.ts:1`, "Generated … Do not edit") |
| client-go | `Routes()` lists every route the client can call, registered at load (`route.go:16-62`) |
| Hypomnema | sqlc-check and openapi-check regenerate and diff (`Makefile:53-67`); the CLIs are pinned with checksums (`agenttool/catalog.go`); `lark.go` and `superset.go` name the version and commit their file came from |
| Onyx | Nothing: the list is hand-kept |

Two checks follow. **Offline, in `make verify`:** every operation of each embedded declaration resolves to a named
action (never `unlisted`), a GET or query to a read and anything else not to a read unless it is on the read list;
and the version each catalog was generated for equals the pinned CLI's (`agenttool.GitHubCLI().Version` and the
others), so bumping a CLI fails until the catalog is regenerated. **At regeneration:** the generator also extracts
what the pinned CLI itself names (gh: the GraphQL root fields in its `graphql:"…"` tags and query strings; glab:
client-go's routes; lark-cli: its registry and shortcuts; gws: its service list) and fails on any that the
declaration lacks. The live signal stays the `serviceproxy.request` log with `action = <integration>.unlisted`
(`proxy.go:372-373`).

## 6. GitLab for a Bot (#705)

- **Which host is GitLab.** The proxy picks a catalog by the Integration's name (`catalog.go:243-264`); a git host's
  Integration is its host name, and its kind is declared (`internal/shared/githost/githost.go:27-39, 129-143`). A
  self-hosted GitLab has its own address, so its routes carry no host and the Connection's host is the one reached,
  as Superset's do (`catalog.go:27-29`).
- **The tool.** glab v1.130.0 from its release with `checksums.txt` (`.goreleaser.yml:140`), the token in
  `GITLAB_TOKEN`, the host in `GITLAB_HOST`, update checks off (`GLAB_CHECK_UPDATE`), and `GITLAB_ACCESS_TOKEN`,
  `OAUTH_TOKEN` unset — the env names read from glab's source; the shape copies `agenttool.GitHubCLI`
  (`agenttool/catalog.go:280-300`).
- **Large clones** stay a worker's job over SSH (#705).
- **The connection on zm** (`git-vmaps-devops.vtii.vn/default`) was not checked from this node.

## Looked for and not found

- A reference that regenerates a policy catalog over stored per-action answers: none (Onyx, github-mcp-server,
  zereight and google_workspace_mcp hand-name their tools; Hypomnema's Lark is the only generated one).
- A REST endpoint for GitHub auto-merge: none in the description; only GraphQL.
- GitLab's own MCP server (inside `gitlab-org/gitlab`) was not read: the repository is too large to clone for this
  note; zereight's is the maintained community server (2.3.28, committed today) and was read instead.
- A reference that treats reading CI variables (GitLab returns their values) as anything but a read: none; zereight
  lists `list_project_variables` and `get_project_variable` as read-only (`tools/registry.ts`, in `readOnlyTools`).
