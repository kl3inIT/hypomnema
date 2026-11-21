# Hypomnema for Orca

An [Orca](https://github.com/stablyai/orca) plugin that shows what waits for you in Hypomnema while you work in
Orca: questions from workers, reviews that stopped at their last round, failed Tasks in open Runs and blocked
Tasks. It is the Work page's "Needs you" column, with:

- a bell and a count in Orca's status bar, hidden when nothing waits; pressing it shows or hides Orca's right
  sidebar, where the panel is (a plugin cannot open its own panel: a command runs only Orca's built-in actions);
- one desktop notification for each new item, never two for the same item, across restarts;
- a Hypomnema panel in the right sidebar: each item with its Run and Task, a question's options as buttons (and a
  text box where an option takes text), and Open, which opens the review's pull request or the Task on the web.

## Install

1. Mint a token for Orca on the control plane's machine. It is an owner token, because answering a question needs
   one; keep it for Orca only, so it can be revoked alone (`hypomnema tokens list` shows its id for
   `hypomnema tokens revoke <id>`):

   ```sh
   hypomnema tokens add orca --kind owner
   ```

2. Install the plugin in Orca from this folder of a clone of the repository. Orca installs a Git URL from the
   repository's root only (a folder inside one needs a catalog entry with its `path`), so the folder is given
   as a path on the machine Orca runs on; to update, pull and install again:

   ```sh
   git clone https://github.com/kl3inIT/hypomnema
   orca plugin install ./hypomnema/orca
   ```

3. Approve it. The plugin runs its own worker (a Node process with your user's access) and asks for
   `storage:own`, `secrets:own`, `settings:own` and `notifications:show`; Orca shows that list and a fingerprint,
   and you approve it once. A new version that asks for more is approved again.

4. Open the Hypomnema panel, type the control plane's URL (the one the web client is on) and paste the token.
   The token goes to Orca's secrets (the OS keyring) and is never shown again; the URL is a setting of the
   plugin, also in Orca's Settings. "Change server or token" in the panel replaces either.

The machine Orca runs on must reach the control plane, as a browser on it reaches the web client.

## Develop

Orca loads the TypeScript as written; there is no build step.

```sh
orca plugin types orca            # the typings in orca/.orca/types, for tsc
npx tsc -p orca
orca plugin test orca             # src/*.test.ts(x), with a fake control plane behind fetch
orca plugin check orca --json     # renders the panel in each src/panel.check.ts scenario
orca plugin validate orca --json
orca plugin dev watch orca        # loads this folder into Orca and follows its logs
```

Some Orca packages lack the typings `orca plugin types` copies: on Linux, 1.4.247 and 1.4.248 fail with `ENOENT:
... resources/plugins/declarative/orca.d.ts`. The same files are in Orca's source at the release's tag, and
`tools/types-from-source.mjs` writes them as `orca plugin types` does:

```sh
git clone --depth 1 --branch v1.4.248 https://github.com/stablyai/orca "$TMPDIR/orca-src"
node orca/tools/types-from-source.mjs "$TMPDIR/orca-src"
npx tsc -p orca
```

`src/waiting.ts` is the web's rule for what waits for the owner (`web/src/features/work/work-state.ts`), copied
because Orca refuses an import outside the plugin; `web/src/features/work/orca-waiting.test.ts` runs both on the
same fixtures, so change them together. The web's Vitest compiles that file with the web's own tsconfig, as
`orca/tsconfig.json` extends typings a clean checkout does not have. `.oxfmtrc.json` formats as Orca's own
examples are formatted (`pnpm --dir web exec oxfmt -c ../orca/.oxfmtrc.json ../orca`).

## Notices

Made with `orca plugin create --template worker-panel` from [Orca](https://github.com/stablyai/orca) (MIT,
Copyright (c) 2026 Lovecast Inc.): its layout, `tsconfig.json`, `.gitignore`, `.gitattributes` and the shape of
`src/protocol.ts`, `src/worker.ts`, `src/panel.tsx` and their tests. The event stream is parsed as the web client's
generated SSE client parses it (`@hey-api/openapi-ts`, MIT).
