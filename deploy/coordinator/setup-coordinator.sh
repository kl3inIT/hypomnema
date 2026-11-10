#!/bin/sh
# Sets up pi, the coordinator, on the control plane's machine (issue #41, PR 3). The owner runs it once with sudo:
#
#   sudo sh setup-coordinator.sh <control plane user> <this directory, with the hypomnema binary added>
#
# It creates the coordinator user, installs Node and pi for it, the root-owned launcher and one sudoers rule that lets
# the control plane's user run only that launcher as the coordinator, a coordinator token and the 9router key in
# files of mode 0600, and the coordinator skill. No secret is passed on a command line or printed. Run it again to
# update pi (package.json and package-lock.json here, which Dependabot updates), the launcher, the binary or the
# instructions; existing secrets are kept.
set -eu

cp_user="${1:?the control plane user, such as dat}"
src=$(cd "${2:?this directory, with the hypomnema binary added}" && pwd)
# Commands run as the coordinator, who cannot enter the caller's directory.
cd /
cp_home=$(getent passwd "$cp_user" | cut -d: -f6)
node_src="${NODE_SRC:-$cp_home/.hermes/node}"
router_env="${ROUTER_ENV:-$cp_home/hypomnema-spike/.env}"
home=/home/coordinator/.hypomnema-coordinator

echo "== user"
id coordinator >/dev/null 2>&1 || useradd --create-home --shell /bin/bash coordinator
install -d -o coordinator -g coordinator -m 0700 "$home" "$home/agent" "$home/sessions" "$home/work" "$home/pi"

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
install -o root -g root -m 0755 "$src/hypomnema" /usr/local/bin/hypomnema
install -o coordinator -g coordinator -m 0600 "$src/AGENTS.md" "$home/agent/AGENTS.md"

echo "== sudoers"
rule=/etc/sudoers.d/hypomnema-coordinator
printf '%s ALL=(coordinator) NOPASSWD: /usr/local/bin/hypomnema-pi\n' "$cp_user" > "$rule.new"
chmod 0440 "$rule.new"
visudo -cf "$rule.new" >/dev/null
mv "$rule.new" "$rule"

echo "== 9router key and pi's models"
if [ ! -s "$home/ninerouter.key" ]; then
  (umask 077; . "$router_env"; printf '%s' "$NINEROUTER_API_KEY" > "$home/ninerouter.key")
  chown coordinator:coordinator "$home/ninerouter.key"
fi
base_url=$(. "$router_env"; printf '%s' "$NINEROUTER_BASE_URL")
cat > "$home/agent/models.json" <<JSON
{
  "providers": {
    "ninerouter": {
      "name": "9router",
      "baseUrl": "$base_url",
      "api": "openai-completions",
      "apiKey": "!cat $home/ninerouter.key",
      "models": [{"id": "cx/gpt-6.1-sol", "name": "GPT 6.1 Sol (9router)", "reasoning": true}]
    }
  }
}
JSON
chown coordinator:coordinator "$home/agent/models.json"
chmod 0600 "$home/agent/models.json"

echo "== coordinator token and skill"
if [ ! -s /home/coordinator/.hypomnema/client.json ]; then
  token_file=$(sudo -u coordinator mktemp)
  # The control plane's own CLI makes the token; only its last line, the token, goes into the file.
  sudo -u "$cp_user" -H sh -c '. ~/hypomnema-cp/env; ~/hypomnema-cp/bin/hypomnema tokens add pi-coordinator --kind coordinator' \
    | tail -1 | sudo -u coordinator tee "$token_file" >/dev/null
  sudo -u coordinator -H /usr/local/bin/hypomnema login --url "http://${CP_ADDR:-127.0.0.1:18990}" --token-file "$token_file"
  sudo -u coordinator rm -f "$token_file"
fi
sudo -u coordinator -H /usr/local/bin/hypomnema skills install --agent agents

echo "done: add HYPOMNEMA_COORDINATOR_COMMAND='[\"sudo\",\"-n\",\"-u\",\"coordinator\",\"/usr/local/bin/hypomnema-pi\"]' to the control plane's env and restart it"
