#!/bin/sh
# Runs the control plane under systemd and has it update itself from each release (issue #279). The owner runs it
# once with sudo, after setup-coordinator.sh has installed /usr/local/bin/hypomnema:
#
#   sudo sh setup-controlplane.sh <control plane user> <its folder, such as /home/dat/hypomnema-cp>
#
# It writes three units, as Beszel's install-hub.sh writes beszel-hub.service and its --auto-update timer:
#   hypomnema-cp.service      the control plane, as its user, with its env file, logging to serve.log;
#   hypomnema-update.service  a oneshot, as root, running the root-owned /usr/local/bin/hypomnema update, which puts
#                             a newer release in place with its launchers (internal/controlplane/selfupdate);
#   hypomnema-update.timer    starts it two minutes after boot and every five minutes after.
# A control plane run in the tmux session hyp-cp, as it was until 2026-11-13, is stopped first. Running it again
# rewrites the units and changes nothing else.
set -eu

cp_user="${1:?the control plane user, such as dat}"
cp_dir="${2:?the control plane folder, such as /home/dat/hypomnema-cp}"

if [ "$(id -u)" != 0 ]; then
  echo "run this with sudo" >&2
  exit 1
fi

case "$cp_dir" in
  /*) ;;
  *) echo "the folder must be an absolute path" >&2; exit 1 ;;
esac

test -x "$cp_dir/bin/hypomnema" || { echo "$cp_dir/bin/hypomnema is not there" >&2; exit 1; }
test -f "$cp_dir/env" || { echo "$cp_dir/env is not there" >&2; exit 1; }
test -x /usr/local/bin/hypomnema || { echo "run setup-coordinator.sh first: /usr/local/bin/hypomnema is not there" >&2; exit 1; }
id "$cp_user" >/dev/null

cat > /etc/systemd/system/hypomnema-cp.service <<UNIT
[Unit]
Description=Hypomnema control plane
After=network-online.target
Wants=network-online.target

[Service]
User=$cp_user
WorkingDirectory=$cp_dir
ExecStart=/bin/sh -c '. "$cp_dir/env" && exec "$cp_dir/bin/hypomnema" serve'
Restart=on-failure
RestartSec=5
StandardOutput=append:$cp_dir/serve.log
StandardError=append:$cp_dir/serve.log

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/hypomnema-update.service <<UNIT
[Unit]
Description=Put a newer release of the Hypomnema control plane in place
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/hypomnema update --user $cp_user --home $cp_dir
TimeoutStartSec=30min
UNIT

cat > /etc/systemd/system/hypomnema-update.timer <<UNIT
[Unit]
Description=Look for a newer release of the Hypomnema control plane

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
UNIT

install -d -o root -g root -m 0700 /var/lib/hypomnema-update

systemctl daemon-reload

echo "== the control plane under systemd"
if runuser -u "$cp_user" -- tmux has-session -t hyp-cp 2>/dev/null; then
  runuser -u "$cp_user" -- tmux kill-session -t hyp-cp
  sleep 2
fi
systemctl enable --now hypomnema-cp.service
systemctl enable --now hypomnema-update.timer

echo "done: the control plane runs as hypomnema-cp.service and updates itself from each release"
echo "see what an update did: journalctl -u hypomnema-update.service"
