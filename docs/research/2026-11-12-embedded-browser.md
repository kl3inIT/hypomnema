# A browser in the web client, shared by every agent

Date: 2026-11-12. Questions from the owner: can the web client embed a browser now; how do T3 Code, OpenMausBot and
the others do it; how can every agent use one session, so that the owner signs in once; which library do agents use;
how is it shown; on which server does the browser run.

Read in `.tmp/`, in their code: T3 Code (commit `b8c2cfad`, 2026-11-09, MIT), OpenMausBot (Apache-2.0), agent-browser
(Vercel Labs, v0.42.0, commit `51b6f8a`, Apache-2.0, cloned for this note), and, more briefly, OpenClaw, Orca,
OpenHands, opencode, Omnigent, Coder, Omnara, Companion and pi-web. "Inferred" marks what no reference states.
"Not verified" marks what was not read to the end.

## 1. Who embeds a browser, and where it runs

| Reference | What it has | Where |
|---|---|---|
| T3 Code | A headless Chrome on the server (the "environment"), streamed to every client that is not the desktop app of that same machine; the desktop app draws its own machine's tabs in an Electron `<webview>` | `apps/server/src/preview/ServerBrowser.ts`, `ServerBrowserStream.ts`, `ServerBrowserContexts.ts`; `apps/web/src/browser/ServerBrowserSurface.tsx`; `docs/user/remote-access.md` L240-290 |
| OpenMausBot | A Chrome on the server's machine, streamed to a canvas in the web and desktop panels; a noVNC viewer, apart, for a whole computer's desktop | `server/browser/cdp/screencast.ts`, `server/routes/browser-viewers.ts`, `src/components/BrowserPanel.tsx`, `src/components/browser/StreamSurface.tsx` |
| OpenClaw | A headless Chromium driven by Playwright, its screencast sent to WebSocket viewers; noVNC for a browser in a sandbox image | `extensions/browser/src/browser/screencast/session.ts` L189-207, `screencast/upgrade.ts` L10-23, `bridge-server.ts` L47-121 |
| Orca | The desktop app's own Chromium page, its screencast sent to the mobile client | `src/main/browser/cdp/screencast/browser-screencast-stream.ts` L147-182, `mobile/src/browser/use-mobile-browser-stream.ts` |
| OpenHands | A still screenshot per agent step; a person cannot act | `src/components/features/browser/browser.tsx` L6-25 |
| opencode, Omnigent | An Electron `WebContentsView` on the person's machine; nothing remote | `packages/gui-plugins/src/browser/chromium.ts` L214-222; `web/src/components/BrowserPane/BrowserPane.tsx` L1-16 |
| Coder | No browser. A proxy to a port of a workspace, by subdomain, with a signed token | `coderd/workspaceapps/proxy.go` L476, L607 |
| Omnara, Companion, pi-web | None | |

The four that show a remote browser do it the same way: the browser runs where the agent's server is, Chrome's own
`Page.startScreencast` produces JPEG frames, a WebSocket carries them to a canvas, and mouse and keys go back as
`Input.dispatch*` calls.

## 2. T3 Code, in more detail

- The server downloads Chrome for Testing's `chrome-headless-shell` (about 120 MB), checked by size and hash, into its
  data directory, and drives it with `playwright-core` (`preview/PreviewBrowser.ts` L24-88, `ServerBrowserContexts.ts`).
- Browser tabs belong to the environment, not to the device: "you and your agents see the same tabs from any device",
  and "agents keep using them while no device is connected" (`docs/user/remote-access.md` L240-245).
- The stream is the WebSocket `/api/preview-stream/ws?threadId=&tabId=`, with upload and download routes beside it.
  A viewer must hold the read scope; it may act only with the operate scope and `interactive` not `false`
  (`ServerBrowserStream.ts` L71-77). One frame is on the wire at a time, the next waits for the viewer's `ack`, and a
  viewer whose socket buffer passes 8 MiB is disconnected (L29-34, L127-140).
- A person takes control before typing into an agent's tab and releases it for the agent to go on; control is reserved
  at once but the running work is drained first (`preview/SessionControl.ts` L25-60).
- Agents drive it through MCP tools named `preview_*` (`packages/contracts/src/previewAutomation.ts`).
- A dev server is opened as `http://localhost:<port>`, since the browser is on the same machine; the ports listed are
  those held by processes the terminals and agents started (`preview/PortScanner.ts`).
- Ubuntu 23.10 and later block the sandbox Chrome runs in; `sudo t3 browser setup` writes an AppArmor profile and
  installs missing libraries. Only an explicit `T3CODE_SERVER_BROWSER_SANDBOX=0` turns the sandbox off
  (`preview/PreviewBrowserHost.ts` L11-75; `ServerBrowserContexts.ts` `launchOptions`).

## 3. One sign-in for every agent

- T3 Code: one Chrome process; a profile is a named, persistent browser context (`Default`, `Incognito`, up to 24 of
  the person's own: `packages/contracts/src/browserProfile.ts`). An agent's new tab opens in the person's default
  profile, so it shares its sign-ins; it gets storage of its own only when no default profile was reported or the
  profile is `Incognito` (`ServerBrowser.ts` L828-838, L1589-1596; `ServerBrowserContexts.ts` `contextFor`).
- OpenMausBot: a bot's profile key is `bot:<id>` by default, `shared:<partition>` when the bot is put on a shared
  profile, `temp:<session>` for a guest (`server/browser/engines.ts` L36-38; `server/browser/chrome/profile.ts`
  L10-12). Each built-in browser lists its signed-in sites with "Sign out" per site (`docs/browser-packaging.md`
  L76-81). For sites that refuse an automated Chrome, such as Google's sign-in, a shared profile can use the person's
  own running Chrome ("My Chrome", L82-96).
- agent-browser: "When several sessions share one Chrome over `--cdp <port>`, cookies and storage are shared, and
  only the tab selection separates the sessions. Add `--pin-tab` so each session sticks to its own tab"
  (`skill-data/core/references/session-management.md` L54-75; `cli/src/native/tab_binding.rs`).

Inferred: both references run on one machine. With agents on several nodes, one sign-in means one Chrome in one place
that agents elsewhere drive from afar, as issue #21 already says ("one browser service that workers drive remotely
(sessions, profiles, logins kept in one place) instead of a browser per node"). A dev server on another node is then
not reachable as `localhost`; that needs a proxy to the node's port, as Coder's `workspaceapps` does.

## 4. The library an agent drives the browser with

| Reference | Library | Note |
|---|---|---|
| T3 Code | `playwright-core`, inside its Node server | Its own tools, viewer and control gate on top |
| OpenClaw | Playwright | |
| OpenMausBot | agent-browser, chosen 2026-09-06 over Playwright MCP, Chrome DevTools MCP, Steel, Stagehand, browser-use, browserless and Lightpanda (`docs/plans/browser-engine.md`) | Pinned, downloaded with a SHA-256 per platform (`server/browser-engine.ts`). Not verified: how far its newer engine (`omb-cdp`, `server/browser/cdp/`) has replaced it |

agent-browser, read in its code: one Rust executable; a daemon per session that speaks CDP to Chrome; an MCP stdio
server whose default `core` profile has the tools `agent_browser_open`, `_snapshot`, `_click`, `_fill`, `_type`,
`_press`, `_screenshot` and the like (`cli/src/mcp.rs` L23-60, L241), and `agent-browser mcp call <tool> <json>` for
one call; a stream server per session, bound to `127.0.0.1` only (`cli/src/native/stream/mod.rs` L328), that accepts
a WebSocket with no `Origin`, a `file://` one or a loopback one (L637-650), sends `frame`, `status` and `tabs`, and
takes `input_mouse`, `input_keyboard`, `input_touch`, `ack` and `config` with `pacing: "ack"`
(`stream/websocket.rs` L623-669, L723). It does not decide between a person and an agent: "Upstream does not enforce
human priority. OMB owns that gate" (OpenMausBot, `docs/plans/browser-engine.md`).

For a control plane written in Go, agent-browser is a process to start; Playwright would bring a Node runtime into it.

## 5. Tried on zm, 2026-11-12

zm (Ubuntu 24.04.4, x86_64, 4 CPUs, 16 GB of memory with 4.8 GB available and its 4 GB of swap full, 24 GB of disk
free) runs the control plane. agent-browser v0.42.0 (`agent-browser-linux-x64`, SHA-256 checked against the release)
was run there in a directory of its own with its own `HOME`, with the Chrome for Testing 156 that Playwright had
already put on the machine, and removed afterwards.

- Chrome's libraries are all installed. Its sandbox does not start: `No usable sandbox!`
  (`kernel.apparmor_restrict_unprivileged_userns` is 1), the case T3 Code's `browser setup` is for. The rest was run
  with `--no-sandbox`, for the probe only.
- A page opens, and the session's stream listens on a loopback port.
- A second session started on the same profile directory fails on Chrome's profile lock: one profile, one Chrome.
- One session launched Chrome with a profile and set a cookie; two more sessions attached with `--cdp <url>
  --pin-tab` each kept its own tab and its own URL, both read the cookie, and each had a stream of its own. Each
  attach left a blank tab behind.
- Memory, counting shared pages once (`Pss`): the headless shell took about 180 MB with one tab and 280 MB with
  three; the full Chrome about 480 MB and 600 MB. (A first figure of 1.7 to 2.2 GB summed resident sizes, which counts
  shared pages once per process.)
- A session that attaches with `open <url>` gets one tab; attaching with `tab new <url>` left a blank tab behind.
- Closing an agent's session leaves its tab open: the tab has to be closed by name.
- A session that is not pinned follows whichever tab was used last, another session's included; the session that
  launched Chrome must be pinned too, or not used to act.
- A cookie set on a site is still there after Chrome is closed and started again on the same profile, closed at once
  or after 35 s.

## 6. A browser on another machine, and what happens when that machine is gone

Asked by the owner: can the browser be on a machine with more memory than zm, or on several, and what if that
machine is killed.

| Reference | Where the browser may be | When that machine is gone |
|---|---|---|
| T3 Code, OpenMausBot | The server's own machine only | Nothing of its own: the server is gone with it |
| OpenClaw | The Gateway's machine by default. A node host on another machine can hold the browser and the Gateway proxies the browser's actions to it; routing "prefers the host" and falls back to a single connected browser node; `gateway.nodes.browser.node` pins one (`docs/tools/browser/remote.md` L12-16, L55-95) | An error. "Launch failures ... stay on that owner; OpenClaw does not replay them on another machine." Profiles are the node's own and are changed only on the node. The Control UI streams the host's browser live and "falls back to screenshots for node-routed browsers" (`docs/tools/browser/profiles.md`) |
| Hermes, OpenClaw | A hosted browser service reached over CDP (Browserbase, Browser Use, Browserless): `hermes-agent/agent/browser_provider.py`; `docs/tools/browser/remote.md` L97-120 | The provider's concern; sign-ins are kept by a third party, and it is paid for |

No reference copies sign-ins between machines or moves the browser to another machine by itself. agent-browser can
save and load a signed-in state as a file (`state save`, `state load`, `--restore` with an encryption key:
`skill-data/core/references/authentication.md` L48-59, L113, L265-274), which OpenMausBot uses to list and remove
sign-ins of a stopped browser (`docs/plans/browser-engine.md` L127-136), not to move them. Inferred: several Chromes
each loaded with a copy would not stay one session, since a site that renews its session in one copy leaves the
others stale.

Stopping an idle browser: T3 Code closes an agent's idle tabs and closes Chrome after five minutes without a context
(`ServerBrowser.ts` L505, L1502); OpenClaw sweeps idle tabs and caps them per session (`browser.tabCleanup`,
`docs/tools/browser/configuration.md` L83-108); an agent-browser daemon ends after an hour idle by default
(`--idle-timeout`).

## 7. What this means for Hypomnema

Nothing exists today; issue #21 names a browser service and has no design. The terminal on a node (issue #116)
already has the pieces a stream needs: a ticket that opens one WebSocket once from the web client's origin, and the
control plane copying bytes both ways. The design is in the issue this note was written for.
