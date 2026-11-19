# Hypomnema

See [ARCHITECTURE.md](ARCHITECTURE.md) for what exists and [docs/vision.md](docs/vision.md) for the goal.

## Using the CLI from another machine

`hypomnema run`, `answer` and `questions` call the client API, so they work from a laptop as well as on the server.

### Reaching the control plane over Tailscale

The control plane listens only on 127.0.0.1. On its machine, `tailscale serve --bg <port>` publishes it to the
owner's tailnet as `https://<machine name>.<tailnet>.ts.net`; nothing is public.

Each other machine (a laptop that runs the CLI, or a node machine) joins the same tailnet once (`tailscale up`,
approved in the owner's Tailscale account) and then uses that URL with `hypomnema login --url` or
`hypomnema-node init`.

Without root, Tailscale can run in userspace networking mode (`tailscaled --tun=userspace-networking`), which is
enough for `tailscale serve`.

The current deployment is described in [ARCHITECTURE.md](ARCHITECTURE.md#where-it-runs).

1. On the control plane, run `hypomnema tokens add <name>` (add `--kind coordinator` for an agent that coordinates its
   own Runs) and save the token it prints (shown once) to a file, mode 0600.
2. On the other machine: `hypomnema login --url <control plane URL> --token-file <token file>`
   (it checks the token and keeps it in `~/.hypomnema/client.json`).
3. Then, for example, `hypomnema run --repo owner/name "<objective>"`. A repository on GitLab is named by its
   URL; a company's GitLab server is declared once with `hypomnema hosts add <host>`, and an account of a host is
   connected with `hypomnema integrations connect <host> --label <name> --token-file <file>` (a token that can push branches and open
   pull requests on the repositories workers work in).

To connect a Google account (Gmail, Calendar, Drive, Docs, Sheets), as Hermes' google-workspace skill sets one up:

1. In the [Google Cloud console](https://console.cloud.google.com/), create a project, enable the Gmail, Google
   Calendar and Google Drive APIs (and Docs and Sheets when wanted), and configure the OAuth consent screen as
   External with yourself as a test user. While it is in "Testing", Google ends a sign-in after 7 days.
2. Create an OAuth client of type "Desktop app" and download its JSON file. Keep the file out of Git.
3. `hypomnema integrations client set google --client-file <file>`: the client's secret goes into the vault.
4. `hypomnema integrations connect google --label work --services gmail:read,calendar`: open the link it prints in
   a browser on any machine, sign in, and paste back the address the browser ends on (a page that does not load).
   A service alone may read and write; with `:read` it may only read.

Bots and workers use only the accounts you give them. To let pi use it in chats, and give it to the Runs it starts:
`hypomnema integrations chats google:work on` (or "Use in chats" on the account in Settings). To give it to a Run
you start yourself: `hypomnema runs create --use google:work ...`. The agent calls Google with the `gws` command,
which Hypomnema installs and signs in on every call. The agent never holds the account's token: each request passes
the control plane, which adds it.

Lark works the same way, with `lark-cli` and `lark:<label>`. A Superset deployment is connected with its address
and a local account (`hypomnema integrations connect superset --label <name> --url <address> --user <name>
--password-file <file>`), and AWS accounts as roles, one Connection a role ([setup](deploy/integrations/aws.md)).
A Bot reads GitHub with `gh` and the github.com token the workers use: `hypomnema integrations chats
github.com:default on`, then `hypomnema bots set <bot> --connections …,github.com:default`; reads run, writes ask.
A GitLab host, gitlab.com or a server declared with `hypomnema hosts add <host> --kind gitlab`, is read the same way
with `glab` and that host's Connection (`<host>:default`).

What an agent may do with an account is set action by action: `hypomnema integrations actions google:work` lists
them, and `hypomnema integrations actions set google:work gmail.send never` changes one to `allow`, `ask` or
`never` (or open the account's "Agents may" line in Settings). By default reading and drafts run on their own, and
what sends, changes, shares or deletes asks you first: the request waits, you see exactly what would be sent, and
you answer on the card in the chat or the Task, or with `hypomnema approvals`, `approvals approve <id>` and
`approvals reject <id>`. A request you answer later still runs when the agent repeats it.

To let your own agent (Claude Code, Codex or pi) coordinate workers, log in with a `coordinator` token and run
`hypomnema skills install`. It installs the coordinator skill into `~/.claude/skills` and `~/.agents/skills`; then
ask the agent to give the work to Hypomnema workers.

### Using your tools and accounts locally

Log in with your **owner** token and the control plane's HTTPS URL as above. The local MCP server reads
`~/.hypomnema/client.json`; do not put the token in either client's MCP configuration. Add it from a workspace
you trust, since this login can use all your Connections and installed tools:

```sh
claude mcp add hypomnema -- hypomnema mcp
codex mcp add hypomnema -- hypomnema mcp
```

Scripts can use the same tools without an MCP client. `--json` lists their schemas or returns the full tool result;
without it, the CLI prints names or text. A tool error returns a nonzero exit status.

```sh
hypomnema tools list --json
printf '{}' | hypomnema tools call whatsapp_chats --args-file - --json
hypomnema tools call superset_dashboards --args-file filters.json
```

Install each service CLI locally and put it on `PATH`. For [lark-cli](https://github.com/larksuite/cli) run
`npm install -g @larksuite/cli`; for [gws](https://github.com/googleworkspace/cli) and [gh](https://cli.github.com/),
follow their installation guides. No separate service login is needed. Name the Connection you set in Hypomnema:

```sh
hypomnema exec lark:me -- lark-cli im chats get --as user --chat-id <chat-id>
hypomnema exec google:work -- gws drive files list
hypomnema exec github.com:default -- gh api user
```

The Lark example uses a user Connection; use `--as bot` with an app Connection. `exec` sets a loopback proxy and
its `SSL_CERT_FILE` for the subprocess; the service's real credential stays on the control plane. On Windows and
macOS, allow the one-time OS prompt to trust the local Hypomnema service proxy CA for your user. To revoke it and
delete its local signing keys, run `hypomnema exec remove-ca`. If `exec` reports that its service hosts changed or
the certificate is expiring, run that command and retry to trust a replacement. Linux requires no installation.
This is not a sandbox: run trusted commands that honor the HTTPS proxy. Tool switches and “Use in
chats” do not limit your owner token. The Connection's action policies still apply, including approvals for
sends. See `hypomnema approvals` and rerun the same command after approving a request that was held.

## The web client

The control plane serves the web client at its own URL, the one the CLI logs in to. Make a user on the control
plane once (`hypomnema users add <name>`, which reads the password from standard input), then open the URL in a
browser and sign in with that name and password.

`/team` shows the Bots and the machines their work runs on. A chat shows its Runs above the composer, with their
Tasks and questions. A Task opens to its question, its result and the worker's transcript.

## Setting up a node machine

A node runs the workers. Do this on the machine that will run them, with the control plane already serving.

The short way, once a release exists: steps 1 and 2 below, then on the control plane run
`hypomnema nodes add <name>` and, on the machine, the line it prints
(`curl -fsSL https://<control plane>/install/node.sh | sh`), pasting the token when asked. That does steps 3, 4 and
6; step 5 is done once on the control plane, for every node. Set `HYPOMNEMA_PUBLIC_URL` on the control plane to the URL nodes reach it at.

By hand:

1. Create a dedicated user without sudo, and work as that user:
   `sudo adduser --disabled-password hypnode && sudo -iu hypnode`
2. Install Claude Code and make sure `claude` is on that user's PATH (`claude --version`). It is not logged in on
   the node: a Claude Code worker reaches Claude through the control plane's model gateway.
3. On the control plane, run `hypomnema nodes add <name>` and save the token it prints (shown once) to a file, mode 0600.
4. Write the node's configuration:
   `hypomnema-node init --url <control plane URL> --token-file <token file>`
   (it goes to `~/.hypomnema-node/config.json`). Check the connection with `hypomnema-node check`.
5. Give the control plane each Claude account once, on the control plane:
   `hypomnema accounts add claude <label> --token <file>` (the token of `claude setup-token`), or
   `hypomnema accounts login claude`. The vault keeps it and the model gateway serves it; it never goes to a node.
   A Claude Code profile names a provider of kind `model_gateway`, and its worker gets only its Dispatch's key. A
   fresh install's starting profiles already do: the provider `model-gateway` and its Claude models come with it,
   so nothing else is applied by hand.
6. Run the daemon: `hypomnema-node run`. To keep it running, `hypomnema-node service` prints a systemd user unit.

To change a node's token, make a new one with `hypomnema nodes token add <node> <name>`, put it in the node's
configuration, restart the node, then revoke the old one (`hypomnema nodes token list <node>`,
`hypomnema nodes token revoke <id>`). `hypomnema nodes drain <node>` stops new work on a node and
`hypomnema nodes rm <node>` removes it once its work has ended.
