# A proxy that holds an account's token, and the owner's approval of a write (2026-11-11)

Read to design issue #154: an agent that never holds an account's token, and a request that writes waiting for the
owner. Clones are in `.tmp/`; pictures of pages without code are in `.tmp/affectui/` and `.tmp/affectui-pages/`.
The earlier notes are [connectors](2026-11-10-connectors.md) and
[secrets for agents](2026-11-07-secrets-for-agents.md); this one adds what was opened again and what was tried.

## What each does

| Project | Where the proxy is | What decides a request | The default for a write | A request that waits |
|---|---|---|---|---|
| Onyx | One service every sandbox goes through (`backend/onyx/sandbox_proxy/server.py`, mitmproxy) | An action catalog as data: a name, a sentence, the routes that are it (`external_apps/providers/actions.py`, `gmail.py`); ALWAYS, ASK or DENY an action (`db/enums.py`) | Reading and drafts ALWAYS; sending, labelling, trashing and anything that names no default ASK; a request that matches no action asks; permanent deletion is not in the scopes asked for | Held up to 180 s (`_await_decision`), then EXPIRED and refused; approve once, approve for the session, reject; evaluated again after approval (`_recheck_approval`); every error refuses |
| Omnigent | The parent process, on the sandbox's machine (`omnigent/inner/credential_proxy.py`) | Policies in code, per service (`policies/builtins/google.py`) | Gmail: read and draft, no send, no modify. Calendar: read only. Drive: write only to files the agent made | A tool call is parked on ASK and evaluated again on the retry (earlier note) |
| Infisical Agent Vault | Beside each sandbox (earlier note) | Hosts, methods and paths a service | | No approval of one request |
| OpenMausBot | (earlier note) | | Sending asks each time | Held up to 9 minutes |
| affectUI | None: a UI kit | | | Layout only: actions grouped by kind, a tally a service, an approval card with what is asked, an edit, reject and approve (pictures of `screens/agent-permissions`, `components/approval-gates`, `components/permission-request`) |

Onyx's `PRE_APPROVAL` is what the owner of a scheduled task approved beforehand
(`alembic/versions/99ecd56cb2ce_scheduled_task_pre_approvals.py`), not an answer given after a request ended.

Onyx's web: the Tools tab of an app lists each action with its sentence and three choices, a search and one setter
for all (`documentation/assets/admins/plugins/craft_app_slack_tools.webp`); the approval card shows what is asked,
the request as labelled lines with long values cut, and the three answers, and collapses to one line once answered
(`web/src/app/craft/components/approvals/ApprovalCard.tsx`, `PayloadView.tsx`).

## What was tried (zm, `gws` 0.22.5, the owner's Gmail; every write refused at the probe)

Three throwaway probes in Go's standard library, each removed after its run.

1. **`gws` through a proxy that decrypts.** With `HTTPS_PROXY`, `SSL_CERT_FILE` naming the proxy's certificate and
   a placeholder for a token: reading mail and the agenda worked, the proxy saw the placeholder and sent the real
   token; a write was held and Google received nothing; without the certificate `gws` refused to connect; alone
   with the placeholder Google refused. `gws` speaks HTTP/1.1 and sets a connect timeout only
   (`crates/google-workspace/src/client.rs`): it waited 200 s for a held request.
2. **A tool's connection carried over a websocket.** A loopback CONNECT listener beside the tool, a websocket to
   the proxy half: 21 calls of a triage in 1.4 s. A node already opens websockets to the control plane's address
   (`internal/node/daemon/daemon.go`, `terminal.go`).
3. **The paths `gws` calls for a write**, which Onyx's catalog would not have matched: a send and a draft go to
   `/upload/gmail/v1/users/me/messages/send` and `/upload/gmail/v1/users/me/drafts` as `multipart/related`; a
   Drive upload to `/upload/drive/v3/files`; Calendar and Drive share `www.googleapis.com`.
4. **The same command twice.** The bytes differ: a message's `Date` and `Message-Id`, and the boundary between
   parts. `From`, `To`, `Subject`, the text, an attachment's content, a file's metadata and content, and an
   event's JSON are the same. A repeat is recognized by the decoded request, not by its bytes.

## What Hypomnema takes

- From Onyx: one proxy as a service; the action catalog as data; ALLOW, ASK, DENY an action with reading and
  drafts allowed and the rest asking; no match asks; fail closed; the three answers; the decoded request on the
  card; evaluating again after approval.
- From Omnigent: a placeholder for a tool that will not send a request without a token.
- From affectUI: the grouping of actions and the one-line tally.
- From this repository: a Dispatch's key and the agent API's rules (issue #103), a worker's question and a chat's
  wake-up, the launcher.
- The owner's own, 2026-11-11, which no reference does: an Approval that is not answered while its request is held
  stays open, and when it is approved later the same request passes once. The hold is 100 s and not Onyx's 180 s,
  to end before an agent's shell gives the command up.

## Found by review after the first commit

A proxy that recognizes an action by method and path must read a request the way the service will. Refused since:
an address in the request line, a path with `.` or `..` segments or an encoded slash, a method-override header or
parameter (Google's APIs honor `X-HTTP-Method-Override`), a query that does not parse strictly. The query sent on
is the parsed one written back, and the host is always the tunnel's. Onyx lists one more that is not handled here
either: an API that echoes a request's headers returns the credential in its answer.
