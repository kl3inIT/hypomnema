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
| [2026-11-07-three-deployables.md](2026-11-07-three-deployables.md) | Three binaries in one module, three trees, three enforced boundaries |
