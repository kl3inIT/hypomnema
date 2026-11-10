# Decisions

Architecture decision records. Record a decision only after it is accepted and its implementation has started.

- Name each file `YYYY-MM-DD-short-slug.md`, so parallel sessions never collide on a number.
- Sections: Context, Decision, Alternatives considered, Consequences, Sources.
- Records are append-only. To change a decision, write a new record that supersedes the old one and add one line at
  the top of the old one pointing to it.

Decisions agreed but not yet implemented live in [vision.md](../vision.md) and the
[research](../research/2026-11-06-platform.md).

| Record | Decision |
|---|---|
| [2026-11-07-two-deployables.md](2026-11-07-two-deployables.md) | Two binaries in one module, the vault inside the control plane, three enforced boundaries |
| [2026-11-07-sqlite.md](2026-11-07-sqlite.md) | SQLite for the control plane: one file, one connection, WAL, private permissions |
| [2026-11-07-build-not-adopt-omnigent.md](2026-11-07-build-not-adopt-omnigent.md) | Build Hypomnema for one owner rather than adopt Omnigent; read its code as a reference |
| [2026-11-09-coordinator-cli-and-skill.md](2026-11-09-coordinator-cli-and-skill.md) | Coordinators drive orchestration with the `hypomnema` CLI and a skill, not MCP; pi keeps its own tools |
| [2026-11-10-model-gateway.md](2026-11-10-model-gateway.md) | The model gateway serves a declared model table, subscriptions included; vendor CLIs go straight to their vendor |
