#!/bin/sh
# Sets up pi, the coordinator, on the control plane's machine (issue #41, PR 3). The owner runs it once with sudo:
#
#   sudo sh setup-coordinator.sh <control plane user> <this directory, with the hypomnema binary added>
#
# It creates the coordinator user, installs Node and pi for it, the root-owned launcher and one sudoers rule that lets
# the control plane's user run only that launcher as the coordinator, a coordinator token and pi's gateway key in
# files of mode 0600, and the coordinator skill. No secret is passed on a command line or printed. Run it again to
# update pi (package.json and package-lock.json here, which Dependabot updates), the launcher, the binary or the
# instructions; existing secrets are kept.
#
# pi reaches its model through the control plane's model gateway (issue #20). Before the first run, the control
# plane's user declares the model and makes pi's key, kept in a file only until this script moves it:
#
#   hypomnema gateway models apply <model>.yaml
#   (umask 077; hypomnema gateway keys add pi --models <model> | tail -1 > ~/hypomnema-cp/pi-gateway.key)
set -eu

cp_user="${1:?the control plane user, such as dat}"
src=$(cd "${2:?this directory, with the hypomnema binary added}" && pwd)
# Commands run as the coordinator, who cannot enter the caller's directory.
cd /
cp_home=$(getent passwd "$cp_user" | cut -d: -f6)
node_src="${NODE_SRC:-$cp_home/.hermes/node}"
gateway_key="${GATEWAY_KEY_FILE:-$cp_home/hypomnema-cp/pi-gateway.key}"
gateway_model="${GATEWAY_MODEL:-gpt-6.1-sol}"
cp_addr="${CP_ADDR:-127.0.0.1:18990}"
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

echo "== tools of the integrations"
# The pinned tools (gws for Google), checked against their checksums, and the scripts pi calls them by. Installed as
# the coordinator, under its own home, so that pi can read them and nothing else of the machine changes.
sudo -u coordinator -H /usr/local/bin/hypomnema tool install --dir "$home"

echo "== sudoers"
rule=/etc/sudoers.d/hypomnema-coordinator
printf '%s ALL=(coordinator) NOPASSWD: /usr/local/bin/hypomnema-pi\n' "$cp_user" > "$rule.new"
chmod 0440 "$rule.new"
visudo -cf "$rule.new" >/dev/null
mv "$rule.new" "$rule"

echo "== gateway key and pi's models"
if [ -s "$gateway_key" ]; then
  install -o coordinator -g coordinator -m 0600 "$gateway_key" "$home/gateway.key"
  rm -f "$gateway_key"
fi
[ -s "$home/gateway.key" ] || { echo "no gateway key: make one into $gateway_key first" >&2; exit 1; }
# The key of 9router, which pi called itself until 2026-11-10; the gateway now keeps it in the vault.
rm -f "$home/ninerouter.key"
cat > "$home/agent/models.json" <<JSON
{
  "providers": {
    "gateway": {
      "name": "Hypomnema gateway",
      "baseUrl": "http://$cp_addr/v1",
      "api": "openai-completions",
      "apiKey": "!cat $home/gateway.key",
      "models": [{"id": "$gateway_model", "name": "$gateway_model (gateway)", "reasoning": true}]
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
  sudo -u coordinator -H /usr/local/bin/hypomnema login --url "http://$cp_addr" --token-file "$token_file"
  sudo -u coordinator rm -f "$token_file"
fi
sudo -u coordinator -H /usr/local/bin/hypomnema skills install --agent agents

echo "pi's model is gateway/$gateway_model: the coordinator profile names that provider and model"
echo "done: add HYPOMNEMA_COORDINATOR_COMMAND='[\"sudo\",\"-n\",\"-u\",\"coordinator\",\"/usr/local/bin/hypomnema-pi\"]' to the control plane's env and restart it"
