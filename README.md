# Hypomnema

See [ARCHITECTURE.md](ARCHITECTURE.md) for what exists and [docs/vision.md](docs/vision.md) for the goal.

## Using the CLI from another machine

`hypomnema run`, `answer` and `questions` call the client API, so they work from a laptop as well as on the server.

1. On the control plane, run `hypomnema tokens add <name>` (add `--kind coordinator` for an agent that coordinates its
   own Runs) and save the token it prints (shown once) to a file, mode 0600.
2. On the other machine: `hypomnema login --url <control plane URL> --token-file <token file>`
   (it checks the token and keeps it in `~/.hypomnema/client.json`).
3. Then, for example, `hypomnema run --repo owner/name "<objective>"`.

## Setting up a node machine

A node runs the workers. Do this on the machine that will run them, with the control plane already serving.

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
