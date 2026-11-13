#!/bin/sh
# Sets up pi, the coordinator, on the control plane's machine (issue #41, PR 3). The owner runs it once with sudo:
#
#   sudo sh setup-coordinator.sh <control plane user> <this directory, with the hypomnema binary added>
#
# It creates the coordinator user, installs Node and pi for it, the root-owned launchers (hypomnema-pi for a chat,
# hypomnema-routine for a Routine's script, issue #211) and a sudoers rule that lets the control plane's user run, as
# the coordinator, only those launchers and the helper of the Bots' folders (issue #255: `hypomnema files serve`
# with its root, which reads a Bot's folder and, for the owner, changes it, issue #297), a coordinator token in a
# file of mode 0600, and the coordinator skill. The launchers read the token's address and run their setup with it;
# pi and a Routine's script run in a bubblewrap box
# (setup-box.sh, issues #260 and #211) with a token of their own and never see this one. No secret is passed on a
# command line or printed. Run it again to update pi
# (package.json and package-lock.json here, which Dependabot updates), the launcher, the binary or the
# instructions; the existing token is kept.
#
# pi reaches its model through the control plane's model gateway (issue #20). Nothing of that is set up here
# (issue #204): the launcher writes pi's models.json as pi starts, from the providers that are the gateway, and the
# control plane makes a gateway key for each pi it starts. A Bot names one of those providers and one of its models.
set -eu

cp_user="${1:?the control plane user, such as dat}"
src=$(cd "${2:?this directory, with the hypomnema binary added}" && pwd)
# Commands run as the coordinator, who cannot enter the caller's directory.
cd /
cp_home=$(getent passwd "$cp_user" | cut -d: -f6)
node_src="${NODE_SRC:-$cp_home/.hermes/node}"
cp_addr="${CP_ADDR:-127.0.0.1:18990}"
home=/home/coordinator/.hypomnema-coordinator

echo "== user"
id coordinator >/dev/null 2>&1 || useradd --create-home --shell /bin/bash coordinator
install -d -o coordinator -g coordinator -m 0700 "$home" "$home/agent" "$home/sessions" "$home/work" "$home/pi" \
  "$home/bots" "$home/run"

echo "== the box"
# Each Bot's pi runs in a bubblewrap box (issue #260): bubblewrap, and the AppArmor profile Ubuntu needs for it.
sh "$src/setup-box.sh" coordinator

echo "== node and pi"
if [ ! -x /opt/hypomnema-node/bin/node ]; then
  cp -a "$node_src" /opt/hypomnema-node
  chown -R root:root /opt/hypomnema-node
  chmod -R a+rX /opt/hypomnema-node
fi
# pi's exact version and its dependencies come from the lockfile here.
install -o coordinator -g coordinator -m 0644 "$src/package.json" "$src/package-lock.json" "$home/pi/"
sudo -u coordinator -H sh -c "cd '$home/pi' && PATH=/opt/hypomnema-node/bin:/usr/bin:/bin npm ci --omit=dev \
  --no-fund --no-audit >/dev/null"
sudo -u coordinator -H env PATH=/opt/hypomnema-node/bin:/usr/bin:/bin "$home/pi/node_modules/.bin/pi" --version

echo "== launcher, binary, instructions"
install -o root -g root -m 0755 "$src/hypomnema-pi" /usr/local/bin/hypomnema-pi
install -o root -g root -m 0755 "$src/hypomnema-routine" /usr/local/bin/hypomnema-routine
install -o root -g root -m 0755 "$src/hypomnema" /usr/local/bin/hypomnema
install -o coordinator -g coordinator -m 0600 "$src/AGENTS.md" "$home/agent/AGENTS.md"

echo "== tools of the integrations"
# The pinned tools (gws for Google), checked against their checksums, and the scripts pi calls them by. Installed as
# the coordinator, under its own home, so that pi can read them and nothing else of the machine changes.
sudo -u coordinator -H /usr/local/bin/hypomnema tool install --dir "$home"

echo "== sudoers"
rule=/etc/sudoers.d/hypomnema-coordinator
{
  printf '%s ALL=(coordinator) NOPASSWD: /usr/local/bin/hypomnema-pi, /usr/local/bin/hypomnema-routine\n' "$cp_user"
  printf '%s ALL=(coordinator) NOPASSWD: /usr/local/bin/hypomnema files serve --root %s/bots\n' "$cp_user" "$home"
} > "$rule.new"
chmod 0440 "$rule.new"
visudo -cf "$rule.new" >/dev/null
mv "$rule.new" "$rule"

echo "== keys of earlier installations"
# pi had a gateway key made by hand until issue #204, and before that the key of 9router, which it called itself
# until 2026-11-10. Neither is read any more; the owner revokes the first with `hypomnema gateway keys revoke`.
rm -f "$home/gateway.key" "$home/ninerouter.key" "$cp_home/hypomnema-cp/pi-gateway.key"

echo "== coordinator token and skill"
if [ ! -s /home/coordinator/.hypomnema/client.json ]; then
  token_file=$(sudo -u coordinator mktemp)
  # The control plane's own CLI makes the token; only its last line, the token, goes into the file.
  sudo -u "$cp_user" -H sh -c '. ~/hypomnema-cp/env; ~/hypomnema-cp/bin/hypomnema tokens add pi-coordinator --kind coordinator' \
    | tail -1 | sudo -u coordinator tee "$token_file" >/dev/null
  sudo -u coordinator -H /usr/local/bin/hypomnema login --url "http://$cp_addr" --token-file "$token_file"
  sudo -u coordinator rm -f "$token_file"
fi
sudo -u coordinator -H /usr/local/bin/hypomnema skills install --agent agents

echo "pi's models are the providers that are the model gateway: a Bot names one of them and one of its models"
echo "the Files panel reads the Bots' folders with HYPOMNEMA_BOT_FILES_COMMAND='[\"sudo\",\"-n\",\"-u\",\"coordinator\",\"/usr/local/bin/hypomnema\",\"files\",\"serve\",\"--root\",\"$home/bots\"]' in the control plane's env"
echo "done: add HYPOMNEMA_COORDINATOR_COMMAND='[\"sudo\",\"-n\",\"-u\",\"coordinator\",\"/usr/local/bin/hypomnema-pi\"]' to the control plane's env and restart it"
echo "for script routines also add HYPOMNEMA_ROUTINE_COMMAND='[\"sudo\",\"-n\",\"-u\",\"coordinator\",\"/usr/local/bin/hypomnema-routine\"]'"
