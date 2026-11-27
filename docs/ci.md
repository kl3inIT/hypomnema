# Continuous integration

[CI](../.github/workflows/ci.yml) runs on pull requests and pushes to `main`.
`Changes` skips downstream jobs for changes confined to Markdown or `docs/`.
`Required` is the branch protection check. It runs after every check, including
both matrices. On a code change it requires every job to succeed. On a
documentation-only change it requires the downstream jobs to be skipped.
A failed or cancelled job makes the gate fail.

| Job | What runs |
| --- | --- |
| Static | `make verify-static` and `make govulncheck`. On a PR, migration checks compare against main and allow time-named migrations. On main they compare against the previous push SHA and refuse time-named migrations. |
| Unit | `make unit`, with the coordinator's bubblewrap setup and pinned ffmpeg and ffprobe required. |
| Race | `make race` partitions all Go packages into API, clients, control plane and other groups. The orchestration root package has its own group on main. Its Unit tests run on PRs. |
| Web checks | `make web-check`, the Zalo bridge tests and build, and the pi extension tests. |
| Web build | One production frontend build. The cache uses the tracked web tree, Node version and runner platform. The job shares `web/dist` as a run artifact. |
| Web e2e | Four Playwright shards run the desktop and phone projects at test-level granularity. A fifth job uses the Go control plane to run the server project with that same embedded web build. |

Race shards restore their own Go build caches and populate them on main. The
Playwright jobs restore the pnpm store and the headless Chromium cache. When a
failed shard outlives the web artifact, rerun all jobs to rebuild the artifact.

To rehearse the main checks against a CI branch, use
`gh workflow run ci.yml --ref <branch>`. A manual dispatch runs the full
matrix and refuses time-named migrations. It compares migrations against
current main when run on a branch, or against its first parent on main.
Releases still wait for the tagged commit's successful **push** CI run and
build their own binaries and bundled assets.

Use `gh run list --workflow ci.yml` to find a run and
`gh run view <run-id> --json jobs` for its job and step timestamps. For the
previous layout, see the [CI research note](research/2026-11-10-ci-and-tests.md).
