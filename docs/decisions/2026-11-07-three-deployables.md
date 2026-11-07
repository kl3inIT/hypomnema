# Three deployables in one Go module, with three enforced boundaries

Date: 2026-11-07. Status: accepted, implemented by issue #3 (repository skeleton).

## Context

Hypomnema runs work on many machines across clouds, reads untrusted text (chat, issues, web pages, tool output)
while agents hold credentials, and keeps the owner's secrets. It is a technical system with little business
vocabulary, built by one owner with agents writing most of the code. The layout has to keep the parts that matter
for security and concurrency apart from the start, without ceremony that protects nothing.

## Decision

- **Three binaries from one module**: `cmd/hypomnema` (control plane), `cmd/hypomnema-node` (on every machine that
  does work, dialing out to the control plane), `cmd/hypomnema-vault` (on its own host, with its own database).
- **Three trees** under `internal/` (`controlplane`, `node`, `vault`), each importing only itself and
  `internal/shared`; flat packages inside each tree, as Omnara's `internal/`.
- **Three enforced boundaries**: between the trees; one writer of execution state (`controlplane/execution` owns the
  generated queries); policy on every state change requested from untrusted input.
- **Enforcement**: `depguard` rules per tree; a full dependency check per binary in the Makefile (the node never links
  PostgreSQL, the control plane or the vault); the gate is `make verify`, run the same way locally and in CI.

## Alternatives considered

- **Modular monolith with business modules** (second-bite's `server/`): each module with its tables, published API
  and an allowed-edge list in ARCHITECTURE.md, checked by `boundaries_test.go`. Right for a product with distinct
  business areas; here most parts own no tables, so the ceremony buys nothing.
- **Flat packages with no checks** (Omnara has only a storage rule): fine with a team reviewing every change; with
  agents writing most code, the boundaries that guard secrets and the node's footprint would erode unseen.
- **One binary with roles** (Teleport, Nomad): simpler to ship, but the node would carry the control plane's and the
  vault's code onto every machine that reads untrusted text.
- **Separate Go modules per deployable** (Kubernetes staging, HashiCorp `api/`): stronger isolation, but versioning
  between them is a cost without external consumers.

## Consequences

- A new boundary or deployable needs a security, placement, scaling or failure reason and its own record.
- `internal/shared` must stay small; the dependency check is what catches a driver leaking through it.
- Inside a tree, packages may import each other freely; the compiler forbids cycles, review does the rest.

## Sources

- [Go repository layout research](../research/2026-11-07-go-repository-layout.md): Kubernetes, Tailscale, CockroachDB,
  Teleport, Grafana, Gitea, Nomad, Temporal, Omnara, with paths.
- [Platform research](../research/2026-11-06-platform.md) §4 and §4.1: why the node and the vault are separate.
- `docs/conventions.md`, Layout and boundaries.
