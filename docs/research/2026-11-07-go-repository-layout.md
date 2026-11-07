# Go repository layout, boundary checks and agent skills

Date: 2026-11-07. Read with `gh api` from each repository's default branch, plus the clones in `.tmp/`. What was
inferred rather than read is marked.

## How large Go repositories keep boundaries

| Project | Layout | Enforcement (path) | Weight |
|---|---|---|---|
| Kubernetes | `pkg/`, `cmd/`, 35 staging modules tied by `go.work` | `hack/verify-import-boss.sh` with about 70 `.import-restrictions` files; `hack/verify-imports.sh` against `staging/publishing/import-restrictions.yaml`; depguard and forbidigo in `hack/golangci.yaml` | Very heavy |
| Tailscale | Flat top level, about 70 directories | A checked-in `depaware.txt` per binary (`cmd/tailscaled/depaware.txt`), checked by `make depaware` in CI; `deps_test.go` with `tstest/deptest.DepChecker{BadDeps}` | Light to medium |
| CockroachDB | `pkg/`, hundreds of packages, Bazel | `pkg/testutils/lint/lint_test.go` (`TestForbiddenImports`), about 20 analyzers in `pkg/testutils/lint/passes/` run by `roachvet` and nogo | Heavy |
| Teleport | One `teleport` binary for its roles; client tools `tsh`, `tctl`, `tbot`; `api/` its own module | depguard in `.golangci.yml`: `client-tools` (no `lib/auth`, `lib/web`, `lib/srv`, "to prevent increasing binary size"), a `main` deny list naming replacements | Medium |
| Grafana | `pkg/` with carved-out `pkg/apimachinery`, `pkg/apiserver` | About 45 depguard rules: `files: **/pkg/apimachinery/**`, deny `github.com/grafana/grafana/pkg` | Light |
| Gitea | `models/`, `modules/`, `services/`, `routers/` | depguard `migrations` rule: migration code must not import `models` | Light |
| Nomad | One binary, `nomad/` server and `client/` agent; `api/` its own module | `GNUmakefile` `check`: `go list --test -f '{{join .Deps "\n"}}'` with grep so `api` pulls no internals; dirty-diff checks after generation; semgrep `.semgrep/fsm_time.yml` bans `time.Now()` in the Raft state machine | Light to medium |
| Temporal | One server binary, `service/{frontend,history,matching,worker}` | depguard bans one package; no rule between services found (inferred from its absence) | Light |
| Omnara (`.tmp/omnara`, `0ef1711`) | Flat `internal/`, 64 packages; `internal/storage` behind named methods | depguard: generated sqlc only in `internal/storage`, test support only in tests, banned `math/rand` and `pkg/errors`; custom `omnaralint`; `Makefile` `verify` with OpenAPI, migration and sqlc checks | Medium |

Conclusion: a few depguard rules per real boundary (Grafana, Teleport, Gitea) are the right size for one owner; the
heavy tools pay off with hundreds of contributors. depguard sees only direct imports, so the repositories that keep a
binary lean also check its full dependency list (Tailscale, Nomad). Adopted in `docs/conventions.md` (Layout and
boundaries).

## Agent skill collections

| Collection | Stars, last commit | License | Judgment |
|---|---|---|---|
| samber/cc-skills-golang | 3,806, 2026-11-01 | MIT | Broad, concrete, scoped to `**/*.go`; the 21 skills vendored in second-bite at `8e899e2` are 3 commits behind, none affecting them |
| spf13/go-skills | 697, 2026-11-05 | MIT | High quality (`go` reads the `go.mod` floor, feature table to Go 1.27) but one large skill overlapping samber |
| cxuu/golang-skills | 192, 2026-06-20 | Apache-2.0 | `go-code-review` links to sibling skills, broken when vendored alone |
| testcontainers/claude-skills | 20, 2026-03-20 | MIT | Official; correct current API (`postgres.Run`, `CleanupContainer`, snapshot and restore) |
| timescale/pg-aiguide | 1,884, 2026-11-06 | Apache-2.0 | Table design and migration SQL; some skills TimescaleDB-specific |
| github/awesome-copilot | 40,881, 2026-11-06 | MIT | `github-actions-hardening` is strong; `go-mcp-server-generator` is a template pinned to an old SDK |
| trailofbits/skills | 8,051, 2026-11-06 | CC-BY-SA-4.0 | Serious security review; share-alike, so install as a plugin, not vendored |
| obra/superpowers | 313,628, 2026-11-03 | MIT | Process skills overlapping the owner's own (`code-review`, `test-audit`, `ship-branch`) |
| `.tmp/omp` `.omp/skills` | — | MIT (repository) | `system-prompts`, `tool-prompt-optimization`: writing prompts and tool descriptions an agent reads |
| `.tmp/genie`, `.tmp/companion` | — | — | `verify`, `review`, `tdd`, `ship-pr-dev`: overlap the owner's skills |

No upstream project used here (River, huma, sqlc, goose, pgx, golangci-lint, `coder/websocket`, the MCP Go SDK)
ships a skill, and no maintained third-party one was found.

## Selection, one source per topic

| Topic | Source | Not taken, because it overlaps |
|---|---|---|
| Go language, style, errors, concurrency, context, testing, database access, security, observability, performance, lint, CI | samber/cc-skills-golang (19 of second-bite's 21) | spf13 `go`, cxuu `go-code-review`, copilot `github-actions-hardening` |
| testcontainers-go | testcontainers/claude-skills | — (samber's testing skill is general) |
| Postgres table design and migration SQL | timescale/pg-aiguide (2 skills) | — (samber's database skill covers Go access only) |
| Prompts and tool descriptions for pi and MCP | omp (2 skills) | — |
| Review, tests' value, shipping, research | the owner's skills | superpowers, genie, companion |

From samber, `golang-project-layout` and `golang-popular-libraries` are left out: layout and libraries are decided in
`docs/conventions.md` and `docs/vision.md`, and the skills would contradict them.
