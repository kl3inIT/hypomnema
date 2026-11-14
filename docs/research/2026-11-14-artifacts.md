# Artifacts: a page a Bot makes and the owner sees in the chat

Date: 2026-11-14. Asked by the owner for the dashboards and diagrams of issue #366: how agents make an HTML page,
dashboard, chart or diagram that is shown in the chat, and how it is kept safe. Read by a sub-agent in the clones
under `.tmp/` (OpenMausBot `6ae15f252`, OpenClaw `7b3c6edf5`, Omnigent `2641a4a`, Hermes `dc965932a0`, AI Elements,
assistant-ui, Onyx, OpenHands); OpenMausBot's sandbox, CSP and tool were read again directly.

## How each does it

| Reference | Made by | Stored, updated | Shown | Calls back | Phone, channels |
|---|---|---|---|---|---|
| OpenMausBot | tool `show_html` with `html`, `jsx` or `path` (a folder with `index.html` and data, up to 5 MB); `app_id` replaces (`server/drivers/agents-app-tools.ts:3-13`) | content-addressed `DATA_DIR/app-docs/<kind>/<sha256>` (`server/app-docs.ts`); the chat message (`kind: "app"`) holds `doc`, `rev` and earlier docs (`server/chat-apps.ts:160-207`) | `srcdoc` iframe, `APP_FRAME_SANDBOX = "allow-scripts allow-forms"`, "never allow-same-origin, allow-popups or allow-top-navigation" (`src/components/app/app-frame-host.ts:6`); CSP meta first (`shared/app-frame/document.ts`, `csp.ts:11`); a folder's files from a token route with a `sandbox` CSP header (`server/page-files.ts:41`) | `send`, `open`, `setContext` over postMessage, after a user click, rate-limited | the same iframe in the apps' WebView; other channels a text fallback; `publish_page` for a public link |
| OpenClaw | tool `show_widget` (an HTML or SVG fragment, wrapped, its JS syntax checked) or a JSON `report`; `pin` puts it on a dashboard (`docs/tools/show-widget.md`) | canvas documents, pinned ones in SQLite `board_widgets`; the same `name` bumps `revision` (`docs/web/dashboard-architecture.md:130-150`) | an outer proxy frame on its own origin, inside it a `srcdoc` frame with `allow-scripts allow-forms`; CDNs cdnjs, jsdelivr, esm.sh, unpkg, fonts; no fetch (`skills/visualize/SKILL.md`) | `openclaw.prompt.send` after a click; pinned widgets read data through bindings | native WebViews; channels without a web view do not get the tool; PNG export |
| assistant-ui `examples/with-artifacts` | tool `artifact` `{title, code}` | tool state in the message, versions | `srcDoc`, `sandbox="allow-scripts allow-forms"` (`app/artifact-surface.tsx:316`) | none | — |
| AI Elements | components only: `Artifact*`, `WebPreview*`, `CodeBlock` | — | `web-preview.tsx:202` loads a URL with `allow-same-origin`: safe only on another origin | — | — |
| Omnigent | the agent writes a file; the viewer previews `.html` | the file | `srcdoc` without same-origin (`web/src/shell/codeViewerHelpers.ts:443`); a new tab is about:blank with a sandboxed frame, never `blob:` or `data:`, which would take the app's origin (:673) | none | — |
| Onyx | session files | files | a token URL (1 h) with header `sandbox allow-scripts; default-src 'none'` (`backend/onyx/server/features/build/static_preview.py:45`) | none | — |
| Hermes `live-dashboard` | the skill writes `dashboard.json` and a self-contained `index.html` | files, rebuilt by a cron | a desktop preview tool, else the path | none | a summary when something changed |

## What they share

- The page is sent by the agent and kept by the host with a revision; sending its id again replaces it
  (OpenMausBot, OpenClaw, assistant-ui).
- It is shown as a card in the chat, in a `srcdoc` iframe whose sandbox allows scripts but never same-origin
  (OpenMausBot, assistant-ui, Omnigent, Onyx): the page has an opaque origin, no cookies, no storage, no call with
  the owner's session. A separate origin is needed only to allow same-origin (OpenClaw's proxy, published sites).
- A CSP meta comes before any byte of the page: `default-src 'none'`, scripts inline or from named CDNs only,
  no fetch but to them, no frames, forms or base.
- Data is embedded, not fetched; live data means a schedule rebuilds the page (OpenMausBot, OpenClaw, Hermes).
- Calls back only through postMessage, after a user click, text only.
- Where there is no web view (a chat channel), a text fallback (OpenMausBot) or no tool (OpenClaw).
