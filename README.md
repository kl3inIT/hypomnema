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

No agent uses an account until you say so. To let pi use it in chats, and give it to the Runs it starts:
`hypomnema integrations chats google:work on` (or "Use in chats" on the account in Settings). To give it to a Run
you start yourself: `hypomnema runs create --use google:work ...`. The agent calls Google with the `gws` command,
which Hypomnema installs and signs in on every call.

To let your own agent (Claude Code, Codex or pi) coordinate workers, log in with a `coordinator` token and run
`hypomnema skills install`. It installs the coordinator skill into `~/.claude/skills` and `~/.agents/skills`; then
ask the agent to give the work to Hypomnema workers.

## The web client

The control plane serves the web client at its own URL, the one the CLI logs in to. Open it in a browser and paste an
owner token to sign in.

`/board` shows the Tasks in flight in three columns: Needs you, Working and Done. `/runs` lists the Runs. A Task opens
to its question, its result and the worker's transcript.

## Setting up a node machine

A node runs the workers. Do this on the machine that will run them, with the control plane already serving.

The short way, once a release exists: steps 1 and 2 below, then on the control plane run
`hypomnema nodes add <name>` and, on the machine, the line it prints
(`curl -fsSL https://<control plane>/install/node.sh | sh`), pasting the token when asked. That does steps 3, 4 and
6; step 5 is still by hand. Set `HYPOMNEMA_PUBLIC_URL` on the control plane to the URL nodes reach it at.

By hand:

1. Create a dedicated user without sudo, and work as that user:
   `sudo adduser --disabled-password hypnode && sudo -iu hypnode`
2. Install Claude Code and make sure `claude` is on that user's PATH (`claude --version`).
3. On the control plane, run `hypomnema nodes add <name>` and save the token it prints (shown once) to a file, mode 0600.
4. Write the node's configuration:
   `hypomnema-node init --url <control plane URL> --token-file <token file>`
   (it goes to `~/.hypomnema-node/config.json`). Check the connection with `hypomnema-node check`.
5. For each account, put its Claude token in a file and record where it is:
   `hypomnema-node accounts set <account id> --token-file <file>`
   (the account id comes from `hypomnema accounts add` on the control plane).
6. Run the daemon: `hypomnema-node run`. To keep it running, `hypomnema-node service` prints a systemd user unit.

To change a node's token, make a new one with `hypomnema nodes token add <node> <name>`, put it in the node's
configuration, restart the node, then revoke the old one (`hypomnema nodes token list <node>`,
`hypomnema nodes token revoke <id>`). `hypomnema nodes drain <node>` stops new work on a node and
`hypomnema nodes rm <node>` removes it once its work has ended.
