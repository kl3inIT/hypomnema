# Vendored skills: sources

Copied unchanged from upstream on 2026-11-07. To update, clone the source again and copy the same folders over.
Selection and the one-source-per-topic rule: [docs/research/2026-11-07-go-repository-layout.md](../../docs/research/2026-11-07-go-repository-layout.md)
and [docs/conventions.md](../../docs/conventions.md#agent-skills). Licenses are in `LICENSES/`.

| Source | Commit | License | Skills |
|---|---|---|---|
| [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) | `bb1d43d` (2026-11-01) | MIT | `golang-code-style`, `golang-naming`, `golang-error-handling`, `golang-concurrency`, `golang-context`, `golang-safety`, `golang-structs-interfaces`, `golang-data-structures`, `golang-design-patterns`, `golang-modernize`, `golang-testing`, `golang-database`, `golang-security`, `golang-observability`, `golang-performance`, `golang-lint`, `golang-continuous-integration`, `golang-dependency-management`, `golang-troubleshooting` |
| [testcontainers/claude-skills](https://github.com/testcontainers/claude-skills) | `5263fe4` (2026-03-04) | MIT | `testcontainers-go` |
| [timescale/pg-aiguide](https://github.com/timescale/pg-aiguide) | `c39008a` (2026-11-06) | Apache-2.0 | `design-postgres-tables`, `postgres-database-migration` |
| [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi) (`.omp/skills`) | `b5a6b2f` (2026-11-07) | MIT | `system-prompts`, `tool-prompt-optimization` |

Left out on purpose: the other samber skills (libraries not used here, and `golang-project-layout` and
`golang-popular-libraries`, which the repository already decides); spf13/go-skills, cxuu/golang-skills,
github/awesome-copilot, obra/superpowers and the skills in genie and companion, which overlap the set above or the
owner's own skills (`code-review`, `test-audit`, `ship-branch`, `research-then-decide`).

## Where the repository wins

- `golang-lint`: the repository's `.golangci.yml` (Omnara's curated set) applies, not the configuration this skill
  recommends.
- samber skills mention skills not installed here (`golang-project-layout`, `golang-popular-libraries`,
  `golang-stretchr-testify`, `golang-benchmark` and others): layout and libraries follow `docs/conventions.md` and
  `docs/vision.md`. testify is not adopted; tests use the standard library until a decision says otherwise.
- `design-postgres-tables`: the TimescaleDB parts (hypertables, compression) do not apply; plain PostgreSQL 18.
- `postgres-database-migration`: migrations are goose SQL files; a released migration is never edited.
- `system-prompts` and `tool-prompt-optimization` apply to prompts and tool descriptions the Brain and workers read,
  not to repository documents, which follow the plain-English rules in `AGENTS.md`.
