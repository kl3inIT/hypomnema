# Continuous integration

[CI](../.github/workflows/ci.yml) runs on pull requests and pushes to `main`.
`Changes` skips downstream jobs for changes confined to Markdown or `docs/`.
`Required` is the branch protection check. It runs after every check, including
the Unit, Race and browser matrices. On a code change it requires every job to succeed. On a
documentation-only change it requires the downstream jobs to be skipped.
A failed or cancelled job makes the gate fail.

| Job | What runs |
| --- | --- |
| Static | `make verify-static`. On a PR, Go code generation checks run when schema, queries, generated files or generator inputs change. Migration checks compare against main and allow time-named migrations. On main they compare against the previous push SHA and refuse time-named migrations. `govulncheck` runs on pushes to `main` and manual dispatches. |
| Unit | `make unit` runs the video packages and the rest of the Go suite in parallel. Both jobs install pinned ffmpeg and ffprobe. The non-video job sets up and requires the coordinator's bubblewrap sandbox. |
| Race | `make race` partitions all Go packages into API, clients, control plane and other groups. PRs run the clients and control-plane groups. Pushes and manual dispatches run all five groups. Unit covers all groups on PRs. |
| Web checks | All code runs lint and unit test the web client. PRs check the generated web API client when its inputs or outputs change. Pushes and manual dispatches also check unused code and formatting and run the Zalo bridge and pi extension checks. |
| Web build | One production frontend build. The build also typechecks the web client. The cache uses the tracked web tree, Node version and runner platform. The job shares `web/dist` as a run artifact. |
| Web e2e | Eight Playwright shards share the desktop, phone and server projects at test-level granularity. Each starts Vite preview and the Go control plane using the shared web build. |

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

## The `review` status

The control plane posts a commit status named `review` on the heads of the pull requests its Tasks made, in the
repositories it holds a token for (issue #1279; [architecture](../ARCHITECTURE.md)). It is `success` on the exact
head a reviewer judged `correct`, `failure` on one judged `incorrect` or `blocked`, and `success` with "no review
needed: quick Task <id>" on each head of a `quick` Task's pull request, found by the Task's branch even after its
Run is finished, while no narrow or broad Task continues it. A new push has none until it is reviewed. A verdict covers only the full head SHA it names.
The token must be allowed to write commit statuses (a classic token's `repo` scope, or "Commit statuses: write").

When the control plane first runs the change, the migration queues each pull request's latest verdict, and the
next minute's pass posts it on those still open at that head: on zm it runs by itself when the control plane
starts the release. Nothing else needs to be run.

A docs-only change needs no review as quick work (or goes straight to `main`), so its Task's `quick` kind is
what says so. A pull request no Task made and no reviewer judged, such as Dependabot's or one from the owner's
own branch, gets no status. Once
`review` is a required check, post it by hand on its head (`gh pr view <n> --json headRefOid`):

```sh
gh api repos/<owner>/<repo>/statuses/<sha> -f state=success -f context=review \
  -f description='no review needed: Dependabot'
```

An admin can also merge past it, as `main` does not enforce its rules on admins.
