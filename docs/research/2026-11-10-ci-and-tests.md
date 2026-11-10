# How others keep CI and tests fast

Date: 2026-11-10. Hypomnema's Verify job took about 8.5 minutes and 15 at worst; the owner asked how others arrange
CI and tests. Read by a Sonnet scout in the clones under `.tmp/`: Coder at b798061, Omnara at 0ef1711, 9router-go at
64c156d, nezha at 3e7d9ba, new-api at bd6bb53, Onyx at da837e6, LiteLLM at 5103d8a0, Gemini CLI at 1930f25, Codex at
f5ac7a7, omp at 660a31d, opencode at 145e948. Infisical's clone has no workflows; CLIProxyAPI, beszel and Bifrost
have nothing to learn here.

## 1. Where Hypomnema's time went

Run 42844549135 (a pull request), the Verify job, 512 s: `make verify` 475 s, of which the race tests 290 s, lint
105 s, `sqlc generate` 34 s, the unit tests 22 s, `go mod tidy -diff` 20 s. The same tests take 22 s without the
race detector; under it the slowest packages are orchestration 136 s, modelgateway 112 s, clientapi 72 s,
controlplane 66 s, vault 58 s. The Web job ran beside it in 55 s. Twelve recent runs took from 3 to 15 minutes.

## 2. What each does

| | Jobs | The race detector | Changes that skip jobs | Caches | A database for a test | Older runs |
|---|---|---|---|---|---|---|
| Coder (`ci.yaml`) | Many side by side: `lint`, `gen`, `fmt`, `test-go-pg`, `test-go-race-pg`, `test-js`, `test-e2e`; a last job `required` | Its own job on every pull request, on a larger runner, beside the plain run | A `changes` job (dorny/paths-filter); `required` runs always and lets skipped jobs pass | Test results reused on pull requests (`-count` empty) and not on `main`; the lint cache keyed by the Go files; only `main` writes caches | A template database named after a hash of the migrations, migrated once, copied per test (`coderd/database/dbtestutil`) | cancelled, except on `main` |
| Omnara (`ci.yaml`) | `verify`, `go-tests` as a matrix of `unit` and `race-unit`, frontend, e2e in three shards | A leg of the matrix on every pull request | none | One cache key per matrix leg; the built lint binary keyed by `tools/ci/go.mod` | A migrated template, copied per test (`internal/testutil/integrationdb/pool.go`) | cancelled for pull requests |
| 9router-go (`ci.yml`) | `lint`, `test`, `race`, `coverage`, `integration`, side by side | Its own job on every pull request: "so a race report names itself in the checks list" | none | setup-go's | none | cancelled |
| nezha | two jobs: a matrix of systems, and race with `-shuffle=on` | its own job | none | none | none | cancelled |
| new-api | backend and frontend | not run | none | setup-go's | none | cancelled |
| Onyx | a matrix of modules | not applicable | `paths-ignore` for documentation, paths-filter elsewhere | test results reused; only `main` writes | not applicable | cancelled |
| Gemini CLI, Codex | shards and a job that gathers the results | not applicable | a skipper; the heavy runs after the merge | | | not on `main` |

LiteLLM shards its tests and has a job that fails when a test directory belongs to no shard; opencode skips draft
pull requests and runs what a change affects.

## 3. Read for Hypomnema

- No reference runs the plain tests and then the race tests in one job: each is a job of its own, side by side.
- The two with a database migrate once and copy; both use Postgres, so for SQLite the copy is a file's, which no
  reference shows.
- Skipping the Go jobs for documentation needs a last job that always reports, or a required check never does.
- Reusing test results trades certainty for speed, in Coder's own words, and only the large repositories do it.
