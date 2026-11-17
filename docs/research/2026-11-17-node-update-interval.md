# How soon an agent takes a new release (issue #783)

On 2026-11-17 the nodes stayed one or two releases behind the control plane for a day: a node looked for a release
30 s after it started and then every one to two days (`internal/node/update.go:32-33` on main 7e1ff65), while the
control plane updates every five minutes (`deploy/controlplane/setup-controlplane.sh:69-70`). This note reads how
four agents decide when to look for a release and when they may restart.

Revisions read, cloned into `.tmp/`: Nezha's agent `nezhahq/agent` cf3c5c9, Beszel `henrygd/beszel` 39f9a07,
Tailscale `tailscale/tailscale` 51a970a, Coder `coder/coder` 3211ca0.

## What each does

| Agent | How often it looks | Jitter | Trigger from the server | When it restarts |
|---|---|---|---|---|
| Nezha's agent | At start, then a random 1440–2880 minutes (`cmd/agent/main.go:90-91`, `:235-250`); `self_update_period` overrides it (`:241-242`) | The random interval itself | The dashboard sends a `TaskTypeUpgrade` task over the link, and the agent updates at once (`cmd/agent/task_dispatch.go:129-130`, `cmd/agent/main.go:672-679`) | At once after the binary is replaced (`os.Exit(1)`, `:236-237`, `:247-248`); nothing open is waited for |
| Beszel's agent | A systemd timer, `OnCalendar=daily` (`supplemental/scripts/install-agent.sh:1440-1450`) runs `beszel-agent update` | `RandomizedDelaySec=4h` (`:1448`) | None: the hub never tells the agent | At once: `systemctl restart beszel-agent.service` after the binary is replaced (`agent/update.go:19-25`, `:114-120`) |
| Tailscale | Connected: never on its own. Not connected: every hour (`feature/clientupdate/clientupdate.go:523-537`) | None | Control says the latest version in every map response (`tailcfg/tailcfg.go:2205-2208`, `ClientVersion` at `:2306-2332`; the client keeps it, `ipn/ipnlocal/local.go:4733-4741`), and starts the update itself with a c2n `POST /update` (`feature/clientupdate/clientupdate.go:250-286`) | Not while an SSH session is open, unless control forces it (`feature/clientupdate/clientupdate.go:276-281`) |
| Coder's agent | Never: the agent does not update itself. Each workspace start downloads the agent from the server it talks to (`provisionersdk/scripts/bootstrap_linux.sh:13`, `:23`), so it is the server's version | — | The server's version is in every response (`codersdk/client.go:108-109`), and the CLI compares it with its own and says when they differ (`cli/root.go:1714-1750`); the agent reports its version at start (`agent/agent.go:1265-1266`) | At the next workspace start |

## What they share

- **The server says the version.** Three of four tell the agent about a newer release over the link it already
  holds: Tailscale's map response carries `ClientVersion`, Coder's every response carries its build version, and
  Nezha's dashboard pushes an upgrade task. Only Beszel relies on a timer alone.
- **A timer stays as the fallback**, about a day: Nezha one to two days, Beszel a day plus up to four hours, and
  Tailscale an hour only when it cannot reach control.
- **Open sessions hold a restart** only in Tailscale (SSH sessions); Nezha and Beszel restart at once.

## What it means for Hypomnema

The control plane and the node are one release (`releases/latest/download` holds `controlplane-<os>-<arch>.txt` and
`<os>-<arch>.txt`), and the control plane stops and starts itself when it updates
(`internal/controlplane/selfupdate/update_unix.go:124`, `:139`), so every node loses its link and says hello again
within seconds (`internal/node/daemon/daemon.go:36-37`, back-off of one to thirty seconds). The hello's answer
carrying the control plane's version, as Tailscale's map response and Coder's header do, reaches every connected
node a few minutes after a release, with no new message and no new timer. The design is on issue #783.
