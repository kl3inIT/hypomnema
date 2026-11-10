# Web UI libraries and consistency in the references (2026-11-10)

Read after the owner asked which libraries the references use that the web client lacks, and how they keep their
UI consistent. Clones are in `.tmp/`. A sub-agent read the `package.json` files and the documents; a line marked
"(agent)" was not opened again. opencode and OpenClaw were left out: their UIs are Solid, not React.

## Libraries the web client does not have

Counted over nine React clients: Omnara, Omnigent, Orca, t3code, Companion, Onyx, Infisical, Coder, Hermes (agent).

| Library | For | Used by |
|---|---|---|
| `@dnd-kit/*` | drag and drop | 8: all but Companion |
| `tailwind-merge`, `clsx` | merging classes in `cn()` | 8 (the web client's components import `cn` from the `cn` package instead) |
| `react-markdown`, `remark-gfm` | rendering markdown | 7 (the web client uses streamdown) |
| `@testing-library/react` | component tests | 6 (already used here, with vitest) |
| `dompurify` | cleaning HTML | 6 |
| `zustand` | client state | 5: Omnigent, Orca, t3code, Onyx, Infisical |
| `@xterm/xterm` | a terminal | 5 |
| `sonner` | toasts | 4: Omnigent, Orca, Infisical, Coder; t3code and Onyx have their own |
| `motion` | animation | 4 |
| `@tanstack/react-virtual` | long lists | 4; Coder uses react-window, t3code `@legendapp/list` |
| `monaco-editor` | a code editor | 4; CodeMirror in 2 |
| `babel-plugin-react-compiler` | the React Compiler | 4: Omnara, Onyx, Coder, Hermes |
| Storybook | a gallery of components | 3: Omnigent, Infisical, Coder |
| `@shadcn/lint` | lint against raw colors and restyled primitives | 3: Orca, Coder, t3code (already used here) |
| `@pierre/diffs` | a diff viewer | 3 |
| i18n (`i18next`, `next-intl`) | translations | 3 |
| `react-hook-form` | forms | 2; Formik with yup in 2; TanStack Form in Omnara only |
| `knip` | finding dead code | 2: Coder, t3code |
| `react-hotkeys-hook` | hotkeys | 1 |
| `msw` | mocking the API in tests | 1 |

Found in none: `react-error-boundary`, `nuqs`, `immer`, `jotai`, `@tanstack/hotkeys`, `react-virtuoso`.

## How they keep the UI consistent

| Practice | Who, and where (agent) |
|---|---|
| Colors and sizes as tokens in one place, light and dark | All six with a large UI. Orca `src/renderer/src/assets/main.css`; Omnara `frontend/apps/web/src/styles/`; Onyx `web/lib/shared/tokens/*.json`; Coder `site/src/index.css`; t3code `apps/web/src/index.css` |
| One folder of primitives that features import | All six: `components/ui` in Omnara, Omnigent, Orca, t3code; Coder `site/src/components/`; Onyx a package, `web/lib/opal/src` |
| Lint that fails on a raw color or a restyled primitive | Orca `config/oxlint-design-system.json`; t3code `vite.config.ts`; Coder `site/.oxlintrc.design-system.jsonc` (a warning); Onyx its own plugin, `web/tools/oxlint/opal` |
| Written UI rules an agent must read | Orca `docs/STYLEGUIDE.md`; Coder `site/AGENTS.md`, `.claude/docs/FRONTEND_PATTERNS.md`; Onyx `web/AGENTS.md`; t3code `docs/internals/web-ui.md` |
| One table component | Omnara `components/data-table/DataTable.tsx`; Coder `Table`, `TableEmpty`, `TableLoader`, `TableToolbar`; Onyx `opal/src/components/table` |
| Shared empty and loading pieces | Omnara `ui/empty.tsx`, `skeleton`, `spinner`; t3code `ui/empty.tsx`; Coder `EmptyState`, `Loader`; Onyx `IllustrationContent` |
| Named layout pieces for a page's header and sections | Omnara `components/layout/`; Coder `PageHeader`, `SettingsHeader`, `Margins`; Onyx `@opal/layouts` |
| One form convention | Orca `settings/SettingsFormControls.tsx`; Coder `Form`, `FormField` with Formik; Onyx `opal/src/form/*Field`; Omnara `ui/field.tsx` |
| One icon set | Orca: lucide only; Onyx: `@opal/icons` only, by lint; Omnara: one file that names every icon |
| A gallery, and pictures compared in CI | Coder: 482 stories, every one photographed (`site/pixel.jsonc`); Omnigent: 60 stories; Onyx: `web/tests/e2e/utils/visualRegression.ts`, only when switched on |
| Screenshots in a pull request | t3code `AGENTS.md`; Omnigent `AGENTS.md` |

Their words (agent):

- Orca, `docs/STYLEGUIDE.md`: "Never hardcode a hex value in component code if a variable already covers it." "Use
  the primitives in `src/renderer/src/components/ui/` before anything custom. Never reimplement headless behavior;
  extend the existing wrapper." "**Icons:** `lucide-react` only, default `size-4`, default stroke, color inherited
  from text."
- t3code, `AGENTS.md`: "`apps/web/src/components/ui` exports own their look. Pick a `variant` or `size`; do not
  restyle one with `className`. ... Layout classes (width, flex, margin, position) belong on the parent." And
  `docs/internals/web-ui.md`: "Don't style a raw `<button>`, `<input>`, `<select>`, or `<textarea>` to look like a
  control a primitive provides."
- Coder, `site/AGENTS.md`: "Views handle loading, error, empty, and refetch states without clobbering user state."
  "Use semantic theme colors and existing Tailwind tokens. Do not use the `dark:` prefix." "Create shared
  components only when reuse is established."
- Onyx, `web/AGENTS.md`: "No built-in Tailwind colors". "Buttons: `Button` from `@opal/components`. No raw
  `<button>`."

Only one reference does each of these: a picture of every story in CI (Coder); layout through a `Section`
component only (Onyx); a list of files excused from the lint that may only shrink (Onyx); lint against text written
in JSX (Onyx).

## For Hypomnema

What the web client already had: tokens in `web/src/index.css`, `components/ui`, and `@shadcn/lint` with
`no-raw-colors`, `no-restyle`, `no-unknown-classes` and `require-static-classes` as errors (`web/.oxlintrc.json`),
and screenshots looked at before a pull request.

What it lacked and at least three references have: written UI rules, one way to build a form, a table, an empty
state and a confirmation, and more than two primitives. [Issue #123](https://github.com/kl3inIT/hypomnema/issues/123)
adds the primitives and moves Settings onto them; the rules are in
[conventions](../conventions.md#web-client).

Not adopted, with the reason: zustand (nothing shares client state that TanStack Query does not hold); Storybook
and pictures compared in CI (three references and two; the scenarios in `web/tests` already take pictures of each
state); a toast library (the Base UI style of shadcn has its own `toast`, to be used when a screen needs one).
