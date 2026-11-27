# Splitting a large Go domain package and keeping the root wiring thin

Date: 2026-11-27. Read in the clones under `.tmp/`: Omnara `07af1ed` (2026-11-11), Coder `c1fec5f6` (2026-11-18),
Gitea `6057629` (2026-11-26), Grafana `4092313` (2026-11-17, sparse clone, some files read with `git show`),
Teleport `1283425` (2026-08-25, sparse clone). What was inferred rather than read is marked. Builds on
[the Go repository layout research](2026-11-07-go-repository-layout.md).

## Hypomnema on main

- `internal/controlplane/orchestration`: about 24,000 non-generated lines in 62 files, one `Service`. Seven domains
  already have a store package (`inboxstore`, `routinestore`, `channelstore`, `meetingstore`, `memorystore`,
  `userstore`, plus `storeerr`). A store imports the generated `orchestration/internal/store`, never the root; a write
  across domains stays in the root, which opens the transaction and calls the store's exported `…Tx(ctx, q, …)`
  functions (`routinestore.GrantTx`, `WaitRunTx`); the root exposes it as `s.Routines()`.
- `internal/controlplane` (the root, 26 files, about 6,700 lines) assembles `serve` (`controlplane.go`, 1,217 lines) but
  also holds feature logic: `gateway.go` 897, `inboxsend.go` 497, `inboxsent.go` 382, `machines.go` 370,
  `hosttokens.go` 368, `audio.go` 322, `web.go` 256, `images.go` 102.

## The references

| Project | Large stateful core over one database | Root wiring | Written rule |
|---|---|---|---|
| Omnara | `internal/storage/store.go` (166 lines) is a facade over one store package per domain (`identitystore`, `executionstore`, `memorystore`, …) sharing one sqlc package `storage/internal/dbsqlc`; a coordinator package opens the transaction and passes it to each store's `…Tx` method (`storage/orglifecycle/orgs_projects.go`) | `cmd/api/main.go`, 524 lines: open, build, serve | None; the structure is the guide |
| Grafana | Package by feature, `pkg/services/<feature>` with the store separate from the logic (`dashboardsnapshots/{database,service}`); migrations central | `pkg/server/wire.go` 103 lines, `server.go` 232 | `contribute/backend/package-hierarchy.md`: "packaged by feature, keeping packages as small as reasonable while retaining a clear sole ownership of a single domain"; "Separate store and logic"; `AGENTS.md`: "business logic in `pkg/services/<domain>/` not in API handlers" |
| Gitea | Layers `models/<domain>`, `services/<domain>`, `routers`; a transaction rides in `ctx` (`models/db/context.go:143`, `db.WithTx`) | `routers/init.go` 216 lines of `mustInit` calls | `docs/guidelines-backend.md`: "cmd → routers → services → models → modules", never the reverse; `docs/guidelines-refactoring.md`: break large refactors into several PRs |
| Coder | One generated `database.Store` (`coderd/database`), `coderd` one package of 189 files; `Store.InTx` (`coderd/database/db.go:31,160`) | `coderd/coderd.go` 2,705 lines, `cli/server.go` 3,263 | `CONTRIBUTING.md`: PRs under about 1,000 changed lines |
| Teleport | Key-value backend (`lib/backend`), interfaces in `lib/services`, implementations in `lib/services/local`; `backend.AtomicWrite` instead of SQL transactions | `lib/service/service.go` 8,010 lines | `rfd/0153-resource-guidelines.md`: a new resource's checklist |

File size: no reference has a file- or package-size rule. Files of 1,500–3,000 lines are common in Coder, Teleport
and Gitea; Omnara's largest is about 1,900. Size alone is not a reason to split.

## Conclusion

The common default for a large core over one database is a shared generated query layer under domain packages, with
one way to write across them (Omnara's coordinator passing the transaction, Gitea's `ctx`, Coder's `InTx`); the
careful three (Omnara, Grafana, Gitea) keep the root to building and routing. Coder and Teleport are the outliers.
Hypomnema's store packages already follow Omnara; the departures are the domains still in the root `Service` and the
feature logic in `internal/controlplane`.

Recommended and approved (owner, 2026-11-27):
1. Move the feature logic out of `internal/controlplane` into each feature's package; the root keeps assembling and the
   server-side commands (Omnara, Grafana, Gitea). Add the rule to `docs/conventions.md`.
2. Then move one self-contained domain, profiles with role routing, into `profilestore`, as the existing stores do,
   and judge from that whether to go on. Runs, Tasks, Dispatches and the scheduler stay in the root as the
   coordinator (Omnara's `orglifecycle` role). Inferred: splitting the tightly coupled core would multiply `…Tx` calls
   and make it harder to follow, not easier.
