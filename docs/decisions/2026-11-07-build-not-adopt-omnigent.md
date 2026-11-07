# Build Hypomnema rather than adopt Omnigent

Date: 2026-11-07. Status: accepted; the build continues (issues #19 to #22).

## Context

Omnigent (`.tmp/omnigent`, `7230afa`, Apache-2.0, Python) overlaps much of the target in [vision.md](../vision.md):
its example coordinator Polly plans, fans work out to Claude Code, Codex or Pi in parallel git worktrees, routes each
diff to a reviewer from another vendor and leaves the merge to the user; hosts dial out to a server
(`omnigent host <url>`); the server can provision a cloud sandbox per session; clients are a terminal, a phone-friendly
web UI, native apps and a REST API; policies ALLOW, DENY or ASK (README). It is a team product: multi-user accounts,
shared sessions, co-driving.

## Decision

Keep building Hypomnema. Omnigent stays a reference for driving CLIs, sandboxes, policies, the credential proxy and
the web client; its code is read as specification, not run as a dependency.

## Alternatives considered

- **Adopt Omnigent** (or use it as the node and worker layer): rejected by the owner. The scope is one owner, and its
  team features (accounts, sharing, co-driving) are weight without use. It keeps work in chat sessions, while
  Hypomnema keeps Runs, Tasks, Dispatches, Gates and Conversations durable in its own store (platform research §3.5
  notes Omnigent's in-memory approvals and turn state and no task graph, read by research agents).
- **A one-day trial before deciding**: proposed, declined.

## Consequences

- Hypomnema owns the orchestration core, vault, model gateway and clients; the cost is the build time Omnigent would
  have saved.
- Patterns already taken from Omnigent stay: capability matrix per runtime, headless drivers, placeholder credentials,
  policy verdicts, spawn bounds, the resilience test lab.

## Sources

- Omnigent README (sections "Why Omnigent?", "Polly and Debby", "Deploy a server"), `docs/session-messaging-tool-boundary.md`.
- [Platform research §3.5](../research/2026-11-06-platform.md), [Dispatch and Conversation](../research/2026-11-07-dispatch-and-conversation.md).
