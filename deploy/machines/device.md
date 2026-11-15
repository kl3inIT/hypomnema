# The phone

An Android phone agents use as they use the shared browser (issue #417): an emulator with the Play Store, on a node of
its own. The owner signs in once, every agent sees and drives it, and the owner watches and takes over from the web.
What the code does is in [ARCHITECTURE.md](../../ARCHITECTURE.md) ("A phone, on a node that holds one").

## Make it

On AWS, a machine made with `--device android`:
[the AWS machine guide, section 5](aws.md#5-a-machine-that-holds-an-android-emulator) gives the command, the sizes
that can run an emulator, what its first boot installs and what it costs. The node downloads the emulator and the
Android 14 Play image (about 2 GB) at the phone's first start:

```sh
hypomnema device start phone    # installs at the first start, then boots; minutes
hypomnema device status         # absent, installing, starting, running, stopped or failed, with what is installed
hypomnema device stop phone     # its disk, with what is signed in on it, stays
```

## Sign in

Open a chat, choose **Phone** in the pane's **+** menu, take control and sign in to Google, the Play Store and the
apps there. Install apps from the Play Store the same way. An agent never signs in to an account; the sign-ins stay
on the phone's disk when it stops, and when its machine stops.

### An app the Play Store calls incompatible

Some apps are refused on the emulator: the Play Store says "This app won't work for your device" and has no Install
button. The toll-payment app VETC (`vn.com.vetc.app`) is one; the reason is not known (tried 2026-11-14 and again on
2026-11-15). It was installed this way, on both days:

1. Install [Aurora Store](https://f-droid.org/packages/com.aurora.store/) from F-Droid with the node's adb, checking
   the APK's SHA-256 against F-Droid's `index-v2.json` (4.8.4 on 2026-11-15). Aurora downloads an app's own files
   from Google's servers.
2. The owner signs in to Aurora with Google in the view; its anonymous sign-in is refused from AWS addresses ("Access
   denied! Are you using VPN or Tor?").
3. Install the app from Aurora. It then refuses to run ("Get this app from Play"), because it checks where it was
   installed from.
4. Pull the files Aurora installed (`pm path vn.com.vetc.app`: `base.apk` and its `split_config.*.apk`) and install
   them again with the Play Store named as the installer:
   `adb install-multiple -r -i com.android.vending base.apk split_config.*.apk`. The app then opens normally.

The node's adb is `~/.hypomnema-node/device/sdk/platform-tools/adb`, serial `emulator-5554`; a Run whose worker lands
on the phone's node can use it.

## Who uses it

Every agent that has the shared browser's tools has the phone's (`device_screen`, `device_tap`, `device_type` and the
others; skill `android-device`). One agent's call runs at a time. While the owner holds control in the view, agents'
calls are answered that the phone is paused.

## The renderer

The emulator draws with `-gpu host` on an Xvfb display, which Mesa's llvmpipe renders on the CPU. It is the only
mode that passed the trial: under SwiftShader the emulator died 4 times out of 4 when the map app's "sign in with"
link opened the toll app's authorize screen, and `swangle_indirect` crashed as the map app started
([research, section 2](../../docs/research/2026-11-14-android-device-for-agents.md)). Another mode can be set to
measure it: `auto`, `software`, `lavapipe`, `swiftshader` or `swangle`. Keep one only once that sign-in flow passes
on it. On the phone's machine, as the node's user (the Machines page's terminal, which closes as the node restarts):

```sh
# set the mode (remove the key, or set "host", to go back)
python3 - lavapipe <<'PY'
import json, os, sys
p = os.path.expanduser("~/.hypomnema-node/config.json")
c = json.load(open(p)); c["device_gpu"] = sys.argv[1]
open(p, "w").write(json.dumps(c, indent=2) + "\n")
PY
systemctl --user restart hypomnema-node   # the node reads its configuration as it starts
```

Then, from anywhere with the CLI, start the phone again so the emulator takes the mode (a running phone is taken
back as it is by the restarted node):

```sh
hypomnema device stop phone && hypomnema device start phone
```

`run/emulator.log` under `~/.hypomnema-node/device/` shows the mode the emulator chose. A value that is not a mode
leaves the phone `failed`, with the modes it can be, in `hypomnema device status`. Each agent's call is logged on
the control plane as `agentapi.device_call` with its `duration_ms`.

## When it runs

| What | When | Started again by |
|---|---|---|
| The phone (the emulator) | Stopped 15 minutes after its last use: a start, an agent's call, or a view while it is open | the next call, view or `device start` |
| Its machine | Stopped 30 minutes after that, through the path of `machines stop`: the phone stopped, no call running and no view open for 30 minutes | the next call or view, which waits for the node (about a minute) |

A call that finds the machine stopped waits up to 2 minutes for it, then says the phone is waking and to try again; a
view waits for as long as it is open. The machine is not stopped while its node runs a Dispatch; the stop is asked
again each minute. A node installed by hand, not made by a provider, is never stopped.

Running, the machine costs about $0.39 an hour; stopped, about $3.84 a month for its disk (Singapore, on demand).
`hypomnema machines stop phone` stops it at once; `machines start phone` starts it.
