# One service proxy in the control plane, not one on each node

Date: 2026-11-11. Status: accepted by the owner; built in
[issue #154](https://github.com/kl3inIT/hypomnema/issues/154). Replaces, for Integrations, the rule held in
[conventions.md](../conventions.md#data-and-security) since 2026-11-07 that a proxy on each node, run as another
OS user than the worker, attaches credentials from a vault Session.

## Context

Since issue #103 an agent given a Connection was handed its short-lived access token, as Companion does, and only
a rule in a skill stopped it from sending mail. The lock that was planned is Infisical's Agent Vault: a session
for each task, and a proxy beside each sandbox that attaches the credential.

Read again when the lock was to be built:

- A worker and its node's daemon run as the same OS user. A proxy on the node is a lock only when it runs as
  another user, which is a root step on every node, by hand, and a second thing to operate there.
- The owner's answers, the requests that wait for him and the record of what was done are in the control plane.
  A proxy on a node needs copies of the first and has to carry the other two back.
- Onyx, whose approval of a single request is what was wanted, runs its proxy as one service that every sandbox
  goes through (`backend/onyx/sandbox_proxy/server.py`).
- A node already opens websockets to the control plane's address, for its socket and its terminal. A tool's
  connection carried the same way worked in a probe with the real `gws` (issue #154, 2026-11-11).

## Decision

- **The service proxy is in the control plane** (`internal/controlplane/serviceproxy`). An account's token never
  leaves the control plane's machine. An agent's tool holds a placeholder; its launcher carries each connection
  the tool opens, as it is, over a websocket to the control plane, which decrypts it with a certificate authority
  of its own, decides what the request is and whether it runs, and only then adds the token.
- **A Dispatch's own key is its vault Session.** The key the agent API gives a Dispatch (issue #103) is what the
  proxy knows a worker by; the coordinator is known by its token. What they may use is asked again for every
  request.
- **A node holds nothing of an Integration**: no token, no key of the proxy's certificate, no policy.
- On the control plane's machine the coordinator already runs as another OS user than the control plane.

## Alternatives considered

- **A proxy on each node, as another OS user (the earlier rule; Infisical).** A lock, at the price of root on every
  node, state to keep the same in two places and one more process on each machine.
- **A proxy on each node, as the node's own user.** No root step, and no lock: a worker of the same user can read
  the proxy's memory.
- **No proxy: the control plane calls the service itself, and an agent calls a tool of the control plane's
  (Omnigent's registry, Omnara, Coder).** A lock, but every service needs its tools written, and the service's own
  CLI with its skills (issue #103's choice) is given up.

## Consequences

- A worker's requests to a service cross the control plane. A read of Gmail took as long through the probe as
  without it within what could be told (about 0.3 s a request, the service's own time).
- The control plane sees an Integration's requests in clear, as Onyx's proxy does.
- When the control plane is down no agent reaches a service; it was already so for a token.
- Lark went behind the proxy the same day (issue #175). A git host does not go behind it (owner, 2026-11-11): a
  worker's git work can be undone and is reviewed in a pull request, and no reference proxies git's own traffic.
- The egress of a worker as a whole (deny by default, per Dispatch) is still not built; this record does not decide
  it.
