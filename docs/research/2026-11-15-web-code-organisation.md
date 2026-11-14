# How the references organise a React client's code (2026-11-15)

Read after the owner asked (2026-11-15) that web parts several features share move to `web/src/components/`, and
for one pass on how the references organise a React client's code. The look is not studied again: it is in
[the web UI practices](2026-11-10-web-ui-practices.md) and [conventions](../conventions.md#web-client).

Clones are in `.tmp/`. Hypomnema's own client was counted with `rg` and a small script over `web/src`. Three
sub-agents read the references. Each finding is marked "(code)" or "(docs)" by what was read, and a line marked
"(agent)" was not opened again. Spot-checked by hand: Coder `contributing/frontend.md:52-89,219-234`,
`site/AGENTS.md:44`, `site/package.json:16,21`; Omnigent `web/.oxlintrc.json:13`; Omnara
`frontend/packages/sdk/openapi-ts.config.ts:118`.

Notes on the clones: Infisical's clone is sparse, so its counts come from `git ls-tree` (agent). The OpenHands clone
is its frontend repository, with code in `.tmp/openhands/src`. Omnara has no `AGENTS.md`, and its `CONTRIBUTING.md`
covers only the backend, so every Omnara line is from code.

## The web client today

`web/src`:

| Folder | Files |
|---|---|
| `api/` | 17: `client.ts`, plus `generated/` from openapi-ts |
| `components/` | 65: `ui/` 63 (shadcn, ignored by the lint), `icons/brand-icons.tsx`, `theme-provider.tsx` |
| `features/` | 234 in 22 folders |
| `hooks/` | 2 |
| `lib/` | 7 |
| `routes/` | 28 |
| `test/` | 2 |

The largest feature folders are chat 40, preview 27, models 20, agents 16, machines 14 and settings 14.

**Routes.** All 28 routes are thin TanStack file routes. Each renders a sidebar and a feature's screen, for example
`routes/settings.models.tsx`: `SettingsSidebar` and `ModelsScreen`. They import settings 16 times and shell 13
times.

**Features import each other.** 143 distinct (feature, imported file) pairs. 21 of the 22 features import another
one; runs is imported by chat and tasks but imports only settings and work. Fifteen pairs of features import each
other both ways (folder level): agents with machines, memory, settings, shell and skills; bots with chat, memory,
routines, settings, shell and skills; chat with routines and workbench; files with preview; shell with workbench.
`import/no-cycle` could not be run here, because oxlint printed nothing on this machine, so cycles between single
files are not counted.

Files imported by several other features:

| File | Importing features | What they take |
|---|---|---|
| `shell/app-shell.tsx` | 8 | `PageHeader`, `PageHeading` (`AppSidebar` from workbench) |
| `agents/agent-avatar.tsx` | 7 | `AgentAvatar`, `AvatarName` |
| `settings/settings-parts.tsx` | 6 | `ErrorAlert`, `ConfirmDialog`, `TextField`, `SettingsSection` |
| `bots/use-bots.ts` | 6 | `useBots` |
| `bots/bots-state.ts` | 6 | `firstBot`, `modelLine`, `memoryEntries` |
| `work/use-work.ts` | 6 | `useWork`, `boardKey`, `questionsKey` |
| `work/work-state.ts` | 5 | `firstLine` (4 of 5), `Card`, `needsYou` |
| `files/files-state.ts` | 5 | `problemOf`, `useObjectURL`, `imageType`, `workspaceKey` |
| `agents/runtime-mark.tsx` | 5 | `RuntimeMark` |
| `agents/model-mark.tsx` | 4 | `ModelMark`, `ProviderMark` |
| `settings/use-integrations.ts` | 3 | `said` (the success toast), `useServices` |
| `settings/settings-shell.tsx` | 3 | `SettingsPage` |
| `agents/profiles-state.ts` | 3 | `errorText`, `splitModel`, `modelKey`, `effortLabel` |
| `approvals/{use-approvals,approval-card}` | 3 each | `usePending`, `PendingApprovals` |
| `agents/use-profiles.ts` | 3 | `useProfiles`, `useModels`, `profilesKey` |

Two importers each: `machines/machine-cells.tsx` (`Meter`, `IndicatorDot`, `StateText`),
`machines/machines-state.ts` (`errorText`, `machineState`), `preview/preview-{loader,kind}`,
`memory/memory-editor.tsx`, `skills/skills-field.tsx`, `questions/question-form.tsx`, `shell/chats.ts`,
`browser/browser-state.ts`, and `chat/chat-state.ts` and `work/fixtures.ts`, which only tests import. One importer
each: `machines/{columns-menu,column-visibility}` (the agents list's view menu and column choice).

**Helpers written more than once.** `errorText` is the same six lines in five files:
`agents/profiles-state.ts:259`, `settings/use-integrations.ts:77`, `machines/machines-state.ts:169`,
`routines/routines-state.ts:189` and `skills/skills-state.ts:120`. `files/files-state.ts:112` `problemOf` is the
same with the code kept.

**Data.** The generated SDK and types are imported straight from features (55 imports of `sdk.gen`, 130 of
`types.gen`). Query keys are hand-written `xKey = [...] as const` constants, 31 of them in 17 files. 48 feature
files call `useQuery` or `useMutation`, mostly in the `use-*.ts` beside each feature, but also in screens (for
example `settings/service-cards.tsx` and `tasks/task-panel.tsx`). There is no `queryOptions()`. The live-update
stream is `work/use-work.ts:61` (`useLiveUpdates`, on the generated `streamEvents`); the chat stream is
`chat/use-chat-stream.ts`.

**Client state.** There is no store library. Module-level stores read with `useSyncExternalStore`:
`auth/session.ts`, `workbench/use-grid.ts`, `files/files-state.ts:244`, `chat/attachments.ts:367`,
`preview/pdf-reader-worker.ts`. Contexts: `chat/{dock,open-file}-context.ts`, `artifacts/open-artifact-context.ts`,
`settings/vault-screen.tsx:84`, `chat/work-log.tsx:15`, `components/theme-provider.tsx`.

**`hooks/` and `lib/`.** `hooks/` has `use-mobile.ts` and `use-highlighted-code.ts`. `lib/` has `utils.ts` (`cn`),
`time.ts`, `model-name.ts`, `highlighter.tsx` and `monaco-appearance.ts`.

**Lint.** `web/.oxlintrc.json` turns on the `import` plugin but no import rule. `no-restricted-imports` bans only
`@/components/ui/native-select`. `src/components/**` is ignored as a whole, so the lint does not read
`components/icons` or `theme-provider` either.

**Tests.** 48 `*.test.ts(x)` files sit beside the code, run by Vitest (`src/**/*.test.{ts,tsx}`). 27 Playwright
specs are in `web/tests`.

**Other facts.**
- Files are kebab-case.
- There are no barrel files.
- The React Compiler is on (`vite.config.ts`, `reactCompilerPreset`).

**Exports no other file names**, from a script that looks for each exported name in the other files. It is not
knip, and it misses dynamic use:
- 71 of 818 exports are named by no other file. Most are exported types or constants used only in their own file.
- A few are not used even in their own file: `hooks/use-highlighted-code.ts` `useHighlightedCode`,
  `lib/highlighter.tsx` `highlightCode`, and `files/files-state.ts` `useDirectory`.
- 13 more exports are named only by tests.

## The references

| | Folder model | Shared parts | Between features | Boundary lint | Data layer | Client state |
|---|---|---|---|---|---|---|
| t3code `apps/web/src` | `components/<feature>` (chat 360, settings 235, …), `routes/` thin, `state/`, `lib/`, `hooks/`, 184 loose files (code, agent) | `components/ui`; web and mobile share `packages/client-runtime` (docs `AGENTS.md:70,189`) | free: chat→settings 10, settings→chat 13 (code, agent) | none by folder; `no-restricted-imports` for single seams such as the raw RPC client and `buttonVariants`, `vite.config.ts:31-57,387-430` (code, agent) | Effect atoms, not Query: `state/query.ts:37` `useEnvironmentQuery`; WebSocket RPC in `packages/client-runtime` (code, agent) | zustand, about 24 stores, scattered (code, agent) |
| Orca `src/renderer/src` | about 70 feature folders under `components/`, no `pages/` (code, agent) | `components/ui` 48 files, `components/shared` 6; "Use the primitives in `components/ui/` before anything custom" `docs/STYLEGUIDE.md:57`; no folders named utils, common or shared-stuff `AGENTS.md:48` (docs, agent) | heavy: native-chat→terminal-pane 24 (code, agent) | none by folder; scripts guard the main and preload processes only, `package.json:232-233` (code, agent) | Electron IPC `window.api.*`; no Query (code, agent) | one zustand `useAppStore` of about 50 slices, `store/index.ts:95` (code, agent) |
| Omnigent `web/src` | layers: `pages/` 86, `components/` 367, `shell/` 386, `hooks/` 256 flat, `lib/` 404 flat (code, agent) | flat `components/` plus `components/ui`; "Search for and reuse existing helpers" `AGENTS.md:8-9` (docs, agent) | every layer, upward too: components→pages 6 (code, agent) | `"import/no-cycle": "error"` `.oxlintrc.json:13` (code, checked); seams for `fetch` and routing at :20-79 (code, agent) | Query; a hand-written `lib/*Api.ts` per resource; hooks in `hooks/`; about 403 inline keys, `queryOptions()` twice (code, agent) | zustand, 3 stores in `store/` (code, agent) |
| Omnara `frontend/apps/web/src` | thin `routes/` plus `components/<domain>` (agents 107, org 64, …) plus generic `components/{ui,data-table,key-value,layout}` (code, agent) | as left | free: 149 import lines (code, agent) | none; `max-lines` 500, `.oxlintrc.json:270-276` (code, agent) | hey-api with its `@tanstack/react-query` plugin, `packages/sdk/openapi-ts.config.ts:118` (code, checked); hand-written hooks over the generated `*Options` and `*QueryKey` in `packages/react/src/domains/*.ts`, 160 hooks (code, agent) | no zustand; 4 contexts (code, agent) |
| Coder `site/src` | `pages/` 1383, `modules/` 291, `components/` 262, `api/`, `hooks/`, `utils/` (code, agent) | components "without Coder specific business logic"; modules "related to multiple parts of the UI"; "If code within a page becomes reusable… consider moving it to src/utils, hooks, components, or modules" `contributing/frontend.md:54-63,82-89,219-234`; "Create shared components only when reuse is established" `site/AGENTS.md:44` (docs, checked) | pages→pages 36 lines; components→pages and components→modules 0 (code, agent) | `dpdm` cycle check in `lint`, `site/package.json:16,21` (code, checked); `no-restricted-imports` group for e2e, `.oxlintrc.jsonc:243-282` (code, agent) | `api/queries/*.ts` (about 50) with key builders and `{queryKey, queryFn}` options, components calling `useQuery(template(id))`; "Components must not call API functions directly" `site/AGENTS.md:15,53` (docs, agent) | contexts only, no zustand (code, agent) |
| OpenHands `src` | `routes/` plus `components/features/*` (34 folders), `components/shared`, `ui/` (code, agent) | `components/shared`; "Prefer named hooks, services, stores, and feature modules over shared-root branches" `.agents/skills/custom-codereview-guide.md:231-234` (docs, agent) | free: 235 lines (code, agent) | none by folder; a `no-restricted-syntax` rule forcing the settings key helpers, `eslint.config.js:366-384` (code, agent) | `hooks/query/query-keys.ts` constants; wrapper hooks in `hooks/query` 92 and `hooks/mutation` 56 (code, agent) | zustand, 26 stores in `stores/` (code, agent) |
| Onyx `web/src` (Next.js) | tiers: `lib/opal` → `refresh-components` → `sections/` (feature composites, entity cards) and `layouts/`; `views/` page bodies; thin `app/` routes, `web/AGENTS.md:8-15` (docs, agent) | the tiers; feature hooks in `lib/<feature>/hooks.ts`, `src/hooks/` the last resort, `AGENTS.md:89-90` (docs, agent) | sections→sections a few; views→views 0 (code, agent) | `no-restricted-imports` group bans the legacy `@/components/*`, `.oxlintrc.json:70-91` (code, agent) | SWR; one key registry `lib/swr-keys.ts` (code, agent) | zustand ×3, 14 providers (code, agent) |
| Infisical `frontend/src` | `pages/<product>/<Page>/{route.tsx, components/}`; shared `components/` (v3 library plus domain parts); `views/` "Reusable page-level UI composed into multiple pages", `AGENTS.md:37-39` (docs, agent) | `components/v3` only, `AGENTS.md:126` (docs, agent) | deep page→page imports exist (code, agent) | `import/no-cycle` off, `.oxlintrc.json:495` (code, agent) | `hooks/api/<domain>/{queries,mutations,types}.tsx`, 115 domains, key factories such as `projectKeys.getProjectById()`, `AGENTS.md:43-49` (docs, agent) | contexts in `src/context/` (8); zustand per page (code, agent) |

Other practices:

| | Barrels | Naming | Tests | React Compiler | knip | Storybook |
|---|---|---|---|---|---|---|
| t3code | 1 ("no barrel" `AGENTS.md:188`) | PascalCase components | co-located, 538 | yes, with a lint ban on manual memo | `knip.jsonc` | no |
| Orca | 3 | PascalCase components, kebab modules | co-located, 1906; e2e in `tests/` | yes, with an exclude list | `config/knip.json`, audit only | no; screenshot fixtures |
| Omnigent | 1 | PascalCase | co-located, 673 | no | no | 59 stories |
| Omnara | 0 in the app | PascalCase components, kebab ui and lib | co-located, 109; e2e 4 | yes | no | no |
| Coder | "Do not use barrel files" `frontend.md:66-67` (docs, checked) | PascalCase | co-located, 352; e2e 41 | per folder, checked by script | `site/.knip.jsonc`, in `lint` | 482 stories |
| OpenHands | 27, mostly library entries | kebab only | mostly `__tests__/`, 814 | no | no | no |
| Onyx | 43 | PascalCase | co-located, 346; e2e in `web/tests` | builds only | no | no |
| Infisical | 618 | PascalCase | co-located, 37 | no | no | 70 stories |
| assistant-ui | 94 (a library) | — | co-located (docs `AGENTS.md:47`) | yes | `knip.ts` with a baseline | no |

All from the sub-agents' reading (agent) unless marked checked.

## What the references agree on

- **Routes are thin.** The page body lives with its feature, not in the route file: t3code, Omnara, Onyx `views/`,
  Infisical `route.tsx`, OpenHands. Hypomnema already does this.
- **One folder of primitives**, plus a place for shared parts that are not primitives. That place is
  `components/` beside `components/ui` in Omnara, Coder, Infisical and Omnigent, `components/shared` in Orca and
  OpenHands, and `sections/` and `layouts/` in Onyx.
- **Features may import each other.** All eight allow it, and none enforces folder boundaries with a lint plugin
  (no eslint-plugin-boundaries, no `import/no-restricted-paths`, no dependency-cruiser). Shared parts never import a
  feature: Coder by convention (0 such imports), Onyx by banning the old tier.
- **`no-restricted-imports` guards single seams**, such as one toast, one fetch or one RPC client: t3code, Omnigent,
  Coder, OpenHands, Onyx.
- **Data hooks are grouped by domain in one data layer**, not spread through screens: Coder `api/queries`,
  Infisical `hooks/api/<domain>`, OpenHands `hooks/query`, Omnara `packages/react/src/domains`, Omnigent `hooks/`,
  Onyx `lib/<feature>/hooks.ts` with one key registry. The six that use a query library do this; t3code and Orca use
  none.
- **Unit tests sit beside the code**, and end-to-end tests are in their own folder: all but OpenHands.
- **Few or no barrels** in the application code: t3code, Orca, Omnigent, Omnara, Coder. Coder and t3code write it as
  a rule. Infisical and Onyx are the exceptions.

## Where they differ

- **One shared tier or two.** Coder splits generic `components/` from product-specific `modules/` and writes the
  test for which one a part belongs in. The others keep one tier, Omnara with subfolders by kind (`layout/`,
  `data-table/`).
- **Cycles.** Omnigent fails the lint on `import/no-cycle`, and Coder runs `dpdm`. Infisical turns the rule off,
  and the rest do not check.
- **Query keys.** Generated from the OpenAPI document by hey-api's TanStack Query plugin (Omnara, the same generator
  Hypomnema uses), hand-written option builders (Coder), key factories (Infisical) or constants (OpenHands, Onyx).
- **knip** in four (Coder, t3code, Orca, assistant-ui). Storybook in three (Coder, Infisical, Omnigent); it was
  ruled out on 2026-11-10.
- **Client state.** zustand in five; contexts only in Coder and Omnara. Its absence here was ruled on 2026-11-10.
