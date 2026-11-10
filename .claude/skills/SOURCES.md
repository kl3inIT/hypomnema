# Vendored skills: sources

Copied unchanged from upstream on 2026-11-07. To update, clone the source again and copy the same folders over.
Selection and the one-source-per-topic rule: [docs/research/2026-11-07-go-repository-layout.md](../../docs/research/2026-11-07-go-repository-layout.md)
and [docs/conventions.md](../../docs/conventions.md#agent-skills). Licenses are in `LICENSES/`.

| Source | Commit | License | Skills |
|---|---|---|---|
| [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) | `bb1d43d` (2026-11-01) | MIT | `golang-code-style`, `golang-naming`, `golang-error-handling`, `golang-concurrency`, `golang-context`, `golang-safety`, `golang-structs-interfaces`, `golang-data-structures`, `golang-design-patterns`, `golang-modernize`, `golang-testing`, `golang-database`, `golang-security`, `golang-observability`, `golang-performance`, `golang-lint`, `golang-continuous-integration`, `golang-dependency-management`, `golang-troubleshooting`, `golang-stretchr-testify` |
| [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi) (`.omp/skills`) | `b5a6b2f` (2026-11-07) | MIT | `system-prompts`, `tool-prompt-optimization` |
| [shadcn-ui/ui](https://github.com/shadcn-ui/ui) (`skills/shadcn`), the official skill of https://ui.shadcn.com/docs/skills | `8ab2771` (2026-11-08), copied 2026-11-10 | MIT | `shadcn` |

Removed on 2026-11-07 with the switch to SQLite (issue #15): testcontainers/claude-skills `testcontainers-go` and
timescale/pg-aiguide `design-postgres-tables`, `postgres-database-migration`.

Left out on purpose: the other samber skills (libraries not used here, and `golang-project-layout` and
`golang-popular-libraries`, which the repository already decides); spf13/go-skills, cxuu/golang-skills,
github/awesome-copilot, obra/superpowers and the skills in genie and companion, which overlap the set above or the
owner's own skills (`code-review`, `test-audit`, `ship-branch`, `research-then-decide`).

## Where the repository wins

- `shadcn`: it runs `shadcn info --json` and may add components with the CLI. The web client's components are on
  Base UI (`web/components.json`, style `base-nova`), `web/src/components/ui` is not linted, and what a screen is
  built from follows the references' code first (`AGENTS.md`). Its `migrate-radix-to-base` sibling is left out: the
  client is already on Base UI.

- `golang-lint`: the repository's `.golangci.yml` (Omnara's curated set) applies, not the configuration this skill
  recommends.
- samber skills mention skills not installed here (`golang-project-layout`, `golang-popular-libraries`,
  `golang-benchmark` and others): layout and libraries follow `docs/conventions.md` and `docs/vision.md`.
- `golang-stretchr-testify` and `golang-testing`: only testify's `require` and `assert` are used, as Omnara does;
  `testify/mock` and `testify/suite` are not (`depguard` refuses them), so their sections do not apply.
- `golang-continuous-integration/assets/claude-code-review.yml` is a template GitHub never runs from here. It installs
  skills with an unpinned `npx skills add` and sets `show_full_output: true`; if it is ever used, pin the install by
  commit, run it in a step with no secrets and no `id-token`, and drop `show_full_output`.
- `system-prompts` and `tool-prompt-optimization` apply to prompts and tool descriptions the Brain and workers read,
  not to repository documents, which follow the plain-English rules in `AGENTS.md`.
