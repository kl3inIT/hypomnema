#!/bin/sh
# Lets the launcher run each Bot's pi in a bubblewrap box (issue #260). setup-coordinator.sh runs it; CI runs it
# before the box's test. Run with sudo, naming the user the box is checked as:
#
#   sudo sh setup-box.sh <user>
#
# It installs bubblewrap, as OpenMausBot's cloud image does (docs/bot-box.md), and checks that the user can make a
# box. Ubuntu 23.10 and later refuse an unprivileged user namespace unless an AppArmor profile allows it
# (kernel.apparmor_restrict_unprivileged_userns = 1); when the check fails for that, it writes and loads a profile
# that lets /usr/bin/bwrap make one, as deploy/browser/setup-browser.sh does for Chrome (T3 Code's profile), and
# checks again. It changes nothing when the box already works, so running it again is harmless. It fails when the
# box still cannot be made: the launcher never runs pi outside it.
set -eu

user="${1:?the user the box is checked as, such as coordinator}"
bwrap=/usr/bin/bwrap
profile=/etc/apparmor.d/hypomnema-bwrap

if [ "$(id -u)" != 0 ]; then
  echo "run this with sudo" >&2
  exit 1
fi

# The namespaces and mounts the launcher's box uses, around a command that does nothing.
check() {
  runuser -u "$user" -- "$bwrap" --unshare-user --unshare-pid --unshare-ipc --unshare-uts --unshare-cgroup-try \
    --die-with-parent --ro-bind /usr /usr --symlink usr/bin /bin --symlink usr/lib /lib --symlink usr/lib64 /lib64 \
    --proc /proc --dev /dev --tmpfs /tmp /usr/bin/true
}

echo "== bubblewrap"
if [ ! -x "$bwrap" ]; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y bubblewrap > /dev/null || { apt-get update > /dev/null && apt-get install -y bubblewrap > /dev/null; }
fi
"$bwrap" --version

echo "== the box"
if check 2> /dev/null; then
  echo "$user can make a box"
  exit 0
fi

if [ "$(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2> /dev/null || echo 0)" = 1 ]; then
  cat > "$profile" << 'PROFILE'
# Written by deploy/coordinator/setup-box.sh: lets bubblewrap make the box each Bot's pi runs in (issue #260).
abi <abi/4.0>,
include <tunables/global>

profile hypomnema-bwrap /usr/bin/bwrap flags=(unconfined) {
  userns,

  include if exists <local/hypomnema-bwrap>
}
PROFILE
  apparmor_parser -r "$profile"
  echo "wrote and loaded $profile"
fi

if ! check; then
  echo "$user cannot make a box with bubblewrap: pi would not start" >&2
  exit 1
fi
echo "$user can make a box"
