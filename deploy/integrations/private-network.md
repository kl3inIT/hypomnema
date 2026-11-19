# A service in a private network

A Connection can reach its service through a proxy (issue #850): a self-hosted service, such as a Superset, that is
only reachable inside a company's VPN. The proxy is a setting of that one Connection; every other Connection goes
direct, and nothing changes the routes or the DNS of the control plane's machine.

When the VPN is a Tailscale network (a tailnet, or a Headscale server of the company's own), a second `tailscaled`
in userspace mode runs beside the control plane and offers a SOCKS5 proxy on the loopback only. It needs no root.

## 1. Run a second tailscaled

Pick a folder and a port of its own, apart from any Tailscale the machine already runs:

```sh
dir="$HOME/hypomnema-cp/vpn-tailscale"
mkdir -p "$dir"
nohup tailscaled --tun=userspace-networking \
  --state="$dir/state" --socket="$dir/tailscaled.sock" \
  --socks5-server=127.0.0.1:1055 >"$dir/tailscaled.log" 2>&1 &
```

## 2. Log in to the company's network

```sh
tailscale --socket="$dir/tailscaled.sock" login --login-server=https://VPN-CONTROL-SERVER --accept-routes
```

It prints a URL: open it in any browser and sign in with the account the company gave for this machine (one
machine, one account, as such networks require). `--accept-routes` lets the proxy reach the private ranges behind
the VPN. Check it:

```sh
tailscale --socket="$dir/tailscaled.sock" status
curl -sS -o /dev/null -w '%{http_code}\n' --socks5-hostname 127.0.0.1:1055 https://SERVICE-HOST/health
```

The SOCKS5 proxy resolves names inside the network, so give the Connection the service's own address, never an IP.

## 3. Point the Connection at the proxy

```sh
hypomnema integrations proxy superset:LABEL socks5://127.0.0.1:1055
```

Or Settings → Integrations → the account → Proxy. A new Connection takes it at once:
`hypomnema integrations connect superset --label LABEL --url https://SERVICE-HOST --user USER --password-file -
--proxy socks5://127.0.0.1:1055`. `off` (or an empty field) sends it direct again.

## While the name does not resolve

If the service's name has no address yet, even through the proxy, give the Connection the address to dial, as
curl's `--resolve` does; its name and its certificate are still checked:

```sh
hypomnema integrations dial-address superset:LABEL 100.64.0.7:443
```

Use an address that answers: a name may have several, and not all of them serve the port. Clear it as soon as the
name resolves: `hypomnema integrations dial-address superset:LABEL off`.

## When it stops

The network's sessions expire (often after 180 days), and the machine drops out of it. Every request of the
Connection then fails with "cannot reach SERVICE-HOST through socks5://127.0.0.1:1055", Routines that read it say "A
routine keeps failing", and the Connection is not marked as needing a new password. Log in again as in step 2; the
proxy and the Connection stay as they are. After a reboot, start step 1 again (the state folder keeps the login).
