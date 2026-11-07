# Two deployables in one Go module, the vault inside the control plane

Date: 2026-11-07. Status: accepted, implemented by issue #3 (repository skeleton).

## Context

Hypomnema runs work on many machines across clouds, reads untrusted text (chat, issues, web pages, tool output)
while agents hold credentials, and keeps the owner's secrets. It is a technical system with little business
vocabulary, built by one owner with agents writing most of the code. The layout has to keep the parts that matter
for security and concurrency apart from the start, without ceremony that protects nothing.

The earlier plan (platform research §4) had a third binary, `hypomnema-vault`, on its own host. What protects secrets
from agents is that workers never hold them: a session per Dispatch and a proxy on the node that attaches
credentials, as Infisical's Agent Vault does. That holds whether the vault is its own process or a package. A
separate vault only adds protection when it runs on another host than the control plane, and on zm all of them would
share one machine; it would cost a host, service-to-service authentication and one more thing that can be down.
Omnara keeps encrypted secrets in its main database with a keyring from the environment
(`internal/secrets`, `internal/config/config.go`).

## Decision

- **Two binaries from one module**: `cmd/hypomnema` (control plane, including the vault) and `cmd/hypomnema-node` (on
  every machine that does work, dialing out to the control plane).
- **Two trees** under `internal/` (`controlplane`, `node`), each importing only itself and `internal/shared`; flat
  packages inside each tree, as Omnara's `internal/`. The vault is `controlplane/vault`, the only package that holds
  the root key.
- **Three enforced boundaries**: between the trees; one writer of execution state (`controlplane/execution` owns the
  generated queries); policy on every state change requested from untrusted input.
- **Enforcement**: `depguard` rules per tree; a full dependency check per binary in the Makefile (the node never links
  PostgreSQL or the control plane); the gate is `make verify`, run the same way locally and in CI.
- On the control plane host, pi runs as its own OS user with a filtered environment, and a node may run workers as a
  third user; none of them can read the control plane's files or process environment (checked on Ubuntu 24.04).

## Alternatives considered

- **A separate vault binary on its own host** (the earlier plan): protects the root key if the control plane host is
  taken. Worth it once the control plane runs somewhere less trusted or serves more than one person; until then it is
  cost without protection. The vault's API is written as a boundary (sessions opened and revoked by Dispatch), so the
  move is a deployment change.
- **Modular monolith with business modules** (second-bite's `server/`): right for distinct business areas; here most
  parts own no tables, so the ceremony buys nothing.
- **Flat packages with no checks** (Omnara has only a storage rule): fine with a team reviewing every change; with
  agents writing most code, the boundaries that guard secrets and the node's footprint would erode unseen.
- **One binary with roles** (Teleport, Nomad): the node would carry the control plane's code onto every machine that
  reads untrusted text.
- **Separate Go modules per deployable** (Kubernetes staging, HashiCorp `api/`): versioning between them is a cost
  without external consumers.

## Consequences

- A new boundary or deployable needs a security, placement, scaling or failure reason and its own record.
- `internal/shared` must stay small; the dependency check is what catches a driver leaking into the node.
- Inside a tree, packages may import each other freely; the compiler forbids cycles, review does the rest.
- The control plane host holds the root key, so every process on it that reads untrusted text runs as another user,
  no secret is passed on a command line (other users can read it in `/proc`), and every local service requires
  authentication: localhost is not a trust boundary once the brain has a shell.

## Sources

- [Go repository layout research](../research/2026-11-07-go-repository-layout.md): Kubernetes, Tailscale, CockroachDB,
  Teleport, Grafana, Gitea, Nomad, Temporal, Omnara, with paths.
- [Secrets for agents](../research/2026-11-07-secrets-for-agents.md): Infisical Agent Vault, Omnara secrets, Omnigent.
- `docs/conventions.md`, Layout and boundaries; Data and security.
