#!/bin/sh
# Lets the shared browser (issue #193) run on the control plane's machine. The owner runs it once with sudo:
#
#   sudo sh setup-browser.sh
#
# Ubuntu 23.10 and later refuse the sandbox Chrome runs in until an AppArmor profile allows it, and a minimal
# image lacks libraries Chrome loads. This writes the profile for the headless Chrome the control plane installs
# under its own directory (<browser dir>/tools/chrome-headless-shell/<version>/...), loads it, and installs the
# libraries that are missing. It changes nothing else, and running it again is harmless. Without it the control
# plane says so when an agent first uses the browser; it never turns the sandbox off by itself
# (HYPOMNEMA_BROWSER_SANDBOX=0 does, and is not needed after this).
#
# The profile and the list of libraries are T3 Code's `t3 browser setup`
# (pingdotgg/t3code, apps/server/src/preview/PreviewBrowserHost.ts at b8c2cfad), MIT License,
# Copyright (c) 2026 T3 Tools Inc.; the full text is in third_party/t3code/LICENSE. Changed: the profile's name.
set -eu

profile=/etc/apparmor.d/hypomnema-chrome-headless-shell

if [ "$(id -u)" != 0 ]; then
    echo "run this with sudo" >&2
    exit 1
fi

echo "== AppArmor"
if [ "$(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null || echo 0)" = 1 ]; then
    # Match Ubuntu's Chrome profile; unconfined adds nothing beyond userns.
    cat > "$profile" <<'PROFILE'
# Written by deploy/browser/setup-browser.sh: lets Hypomnema's headless browser use Chrome's sandbox.
abi <abi/4.0>,
include <tunables/global>

profile hypomnema-chrome-headless-shell /**/tools/chrome-headless-shell/*/*/chrome-headless-shell flags=(unconfined) {
  userns,

  include if exists <local/hypomnema-chrome-headless-shell>
}
PROFILE
    apparmor_parser -r "$profile"
    echo "wrote and loaded $profile"
else
    echo "this machine does not restrict the sandbox: nothing to do"
fi

echo "== libraries"
if command -v apt-get > /dev/null 2>&1; then
    # Ubuntu 24.04 and Debian 13 use some t64 library names; the first name apt knows is taken.
    wanted=""
    for names in libnss3 "libglib2.0-0t64 libglib2.0-0" "libatk1.0-0t64 libatk1.0-0" \
        "libatk-bridge2.0-0t64 libatk-bridge2.0-0" "libatspi2.0-0t64 libatspi2.0-0" libdbus-1-3 libx11-6 libxcb1 \
        libxcomposite1 libxdamage1 libxext6 libxfixes3 libxrandr2 libxkbcommon0 libgbm1 \
        "libasound2t64 libasound2" libexpat1; do
        for name in $names; do
            if dpkg -s "$name" > /dev/null 2>&1; then
                continue 2
            fi
        done
        for name in $names; do
            if apt-cache show "$name" > /dev/null 2>&1; then
                wanted="$wanted $name"
                continue 2
            fi
        done
        echo "no package is known for: $names" >&2
    done

    if [ -n "$wanted" ]; then
        # shellcheck disable=SC2086 # A list of package names.
        apt-get install -y --no-install-recommends $wanted
    else
        echo "every library is installed"
    fi
else
    echo "no apt-get here: install Chrome's libraries with this system's own package manager"
fi
