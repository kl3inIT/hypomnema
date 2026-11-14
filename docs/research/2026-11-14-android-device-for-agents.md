# An Android phone for agents, on a cloud machine

Date: 2026-11-14. Question from the owner: how do other agent tools give agents an Android phone or emulator, and
what is the best-performing way for Hypomnema, combining the references.

Read in `.tmp/`, in their code: OpenMausBot (Apache-2.0), T3 Code (MIT) with the `expo-device-hub` it installs
(hub MIT, `serve-emu` Apache-2.0, cloned for this note), Orca (commit `be8f5ace`, 2026-11-11, MIT), Omnigent,
OpenClaw, budtmo/docker-android, HQarroum/docker-android, google/android-emulator-container-scripts, NetrisTV/ws-scrcpy,
redroid, mobile-mcp, agent-device, mobilerun (was droidrun), OpenHands and Codex; the others from their official pages.
Stars and last pushes were read with `gh api repos/<r>` on 2026-11-13. Trials ran on 2026-11-13 and 2026-11-14 on an
AWS instance with nested virtualization, against a Flutter map app and a Flutter toll-payment app it signs in with
(the owner's); the results are kept in the owner's device-test skill, section "Route 3", outside the repository.
"Inferred" marks what no reference states. "Not verified" marks what was seen only in a secondary source or not read
to the end.

## 1. Who gives agents a phone, and how

| Reference | How the agent sees | How it acts | How a person watches | Lifecycle | Path or URL |
|---|---|---|---|---|---|
| OpenMausBot | `read_screen` = `uiautomator dump` parsed to text, desc, id, bounds; `screenshot` = `adb exec-out screencap -p` | First-party stdio MCP: `tap_text`, `tap`, `swipe`, `type_text` (`input text` for `[A-Za-z0-9 _.,@-]`, else ADBKeyBoard), `press`, `open_app` | Electron panel polls a PNG every 850 ms; clicks go back as `adb input`. Not in the web build | Physical USB phone only ("no emulators, wireless or network ADB"); one turn holds the phone, a second gets 409 | `server/drivers/phone-proxy.ts` L1, L123-126, L148-160, L242-249, L262-305, L319; `src/components/AndroidDevicePanel.tsx` L98-200; `electron/android-device.mjs` L171-246 |
| T3 Code | `device_screenshot`; driving through the pinned `agent-device` CLI: accessibility snapshot with `@e12` refs, coordinates last | Four MCP tools (`device_list`, `device_open`, `device_screenshot`, `device_close`); `device_open` returns an `agent-device` command line pinned to the device | Device panel: H.264 over one WebSocket decoded with WebCodecs, WebRTC when advertised; needs a secure page | Boots an AVD the user created, on the server's machine or an SSH device host; one session per device per thread; `device_close` ends it | `docs/user/devices.md` L29-154; `docs/internals/devices.md` L9-156; `apps/server/src/mcp/toolkits/device/tools.ts` L30-80; `apps/server/src/device/deviceToolVersions.json` (`agent-device 0.21.31`, `expo-device-hub 0.19.1`); `apps/web/src/components/device/DeviceStreamView.tsx` |
| Orca | `orca emulator ax` = `uiautomator dump` as a tree. No Android screenshot verb for the agent | `tap x y` (normalized 0..1), `gesture`, `type` (US-ASCII), `button`, `install`, `launch`, `permissions`, `logcat`, `exec` | Electron pane: scrcpy-server 2.4, H.264, WebCodecs `optimizeForLatency: true`; agent and person share the scrcpy control socket | Boots an existing AVD with `-no-window`; one active device per worktree; `kill` when done | `skill-guides/orca-emulator-android.md` L19-99; `src/cli/specs/emulator.ts`; `src/main/emulator/android/android-avd-boot.ts` L68-134; `scrcpy-server-deploy.ts` L3-65; `use-emulator-video-stream.ts` L68, L223-242 |
| Google Android CLI | `android layout` (hierarchy JSON); `android screen capture --annotate` = numbered boxes found by vision, `screen resolve #N` gives x,y | `android input tap/swipe/scroll/text/key` | Not covered | `android emulator create/start/stop/list`; `android device remote` reserves real phones billed to a GCP project | [overview](https://developer.android.com/tools/agents), [screen capture](https://developer.android.com/tools/agents/android-cli/commands/screen_capture), [device remote](https://developer.android.com/tools/agents/android-cli/commands/device_remote) |
| Journeys, Gemini Agent Mode (Android Studio) | Screenshots and reasoning; Agent Mode also Logcat | `adb shell input` | Studio's Running devices window; a results panel with the screenshots sent to the agent | The connected device | [Journeys](https://developer.android.com/studio/gemini/journeys), [agent mode](https://developer.android.com/studio/gemini/agent-mode) |
| Devin | Computer use on the emulator window on its Linux desktop, plus adb screenshots | Computer-use clicks; adb for install, tests, logcat | Desktop tab in the web app, and recordings | "Every session boots from that snapshot" with SDK and AVD baked in; `-no-window -no-audio -gpu swiftshader_indirect`, pixel_6, API 34 | [docs](https://docs.devin.ai/onboard-devin/environment/android-emulation), [blog](https://cognition.com/blog/android-emulator) |
| mobile-mcp, agent-device, mobilerun, Android-MCP | Accessibility tree first, screenshot fallback; mobilerun `--vision-only` for apps without a tree; mobile-mcp reads a debuggable Flutter app's render tree from the Dart VM service | adb or an accessibility service | None | None of their own | mobile-mcp `README.md` L7, L77, L124-129, `CHANGELOG.md` 1.0.3; mobilerun `README.md` L106-154; [Android-MCP](https://github.com/CursorTouch/Android-MCP) |
| budtmo/docker-android, Google container scripts | Appium; a small adb MCP in budtmo (`mcp/src/server.py`) | Appium, adb | budtmo: noVNC on port 6080; Google: WebRTC from the emulator's gRPC | Container; budtmo destroys the device on restart unless `/home/androidusr` is a volume; Google is fresh each start (`-no-snapshot-save -wipe-data`) | budtmo `docker/emulator/Dockerfile` L25-62, `README.md` L101-106; Google `README.md` L236-262, `emu/templates/launch-emulator.sh` L180 |
| BrowserStack, AWS Device Farm, Sauce Labs | Screenshot; Appium-style `find_element` | Appium actions | Vendor dashboard, live, with video | Leased real devices: BrowserStack 90 s idle, 2 h max; Device Farm 150 min (not verified) | [BrowserStack MCP](https://www.browserstack.com/docs/browserstack-mcp-server/tools/mobile-browser-control), [Device Farm](https://docs.aws.amazon.com/devicefarm/latest/developerguide/remote-access.html) |
| OpenClaw | No agent control of an Android UI; the Android app is a node exposing `camera.snap`, `notifications.*` and the like | | scrcpy over ADB-over-TCP, as an operator recipe apart from the node | | `docs/platforms/android.md` L172-263, L548-596 |
| Omnigent, OpenHands, Codex | Nothing: Omnigent's Android folder is a WebView shell of its own UI; `rg` for emulator, scrcpy, uiautomator in OpenHands and Codex finds nothing | | | | `omnigent/web/android/README.md` L1-11 |

Every tool that drives an app gives the agent a structure read and a screenshot. No reference gates a person taking
over the device from the agent: in T3 Code and Orca both type at once (T3 searched for "take control|takeover|claim";
Orca's `emulator-pane-input-check.ts` guards only iOS).

## 2. Running the emulator on a cloud machine without a GPU

**KVM.** The emulator needs KVM; `-accel off` "is not supported and is very slow"
([command line](https://developer.android.com/studio/run/emulator-commandline)). Since 2026-02-16 AWS offers nested
virtualization on non-metal instances ([What's New](https://aws.amazon.com/about-aws/whats-new/2026/02/amazon-ec2-nested-virtualization-on-virtual)):
the user guide lists M7i, M7i-flex, M8i, C7i, C7i-flex, C8i, R7i, R8i and their variants, Intel only, at no extra cost,
enabled with `--cpu-options NestedVirtualization=enabled`, and still advises bare metal for latency-sensitive work
([user guide](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html)). No Graviton
type has it, and Google publishes no linux_aarch64 emulator build (`dl.google.com/android/repository/repository2-3.xml`).
Two earlier cloud VMs of another provider failed with `KVM: entry failed, hardware error 0x8` (the owner's skill).

Singapore Linux on demand, from AWS's price feed published 2026-11-10:

| Type | vCPU / GiB | $/h | About $/month (730 h) |
|---|---|---|---|
| c7i-flex.large | 2 / 4 | 0.0978 | 71 |
| c7i-flex.xlarge | 4 / 8 | 0.1956 | 143 |
| m7i-flex.xlarge | 4 / 16 | 0.2394 | 175 |
| c8i.large, m8i.large | 2 / 4, 2 / 8 | 0.1081, 0.1323 | 79, 97 |
| c6g.metal (Graviton, cheapest metal) | 64 | 2.51 | 1,831 |
| g4dn.metal (the only AWS type with a GPU and KVM) | | about 11 | |

The trials used c7i-flex.2xlarge (8 vCPU, 16 GB, 40 GB gp3); its price was not read from the feed (inferred: about
twice the xlarge's). Redroid needs no KVM but needs `binder_linux` and `--privileged`, and a map app's renderer hung
on its software GPU in an earlier trial (redroid `README.md` L49-50; the owner's skill).

**GPU modes.** The official page lists `auto`, `host`, `software`, `lavapipe` ("software rendering with Mesa 3D
drivers"), `swiftshader` and `swangle`, and says `guest`, `swiftshader_indirect` and `swangle_indirect` are deprecated
since 36.4.9 ([acceleration](https://developer.android.com/studio/run/emulator-acceleration)). The references still
use the old names: Devin and HQarroum `swiftshader_indirect` (`scripts/start-emulator.sh` L46-79), serve-emu
`-gpu host` by default and `swiftshader_indirect` "for headless hosts without a usable GPU", and says llvmpipe or
lavapipe "caps the guest at a janky ~20fps" (`packages/serve-emu/src/emulator.ts` L38-46, README L193-211); budtmo
defaults to `software` (`cli/src/device/emulator.py` L79, L182). Google's scripts use `-gpu host` only with NVIDIA and
Xvfb (`run-with-gpu.sh` L22-30).

**Measured on 2026-11-13 and 14** (Android 14 `google_apis_playstore` x86_64, AVD 720x1280 at 320 dpi):

| Setting | Map panning |
|---|---|
| 1080x2400, 4 vCPU, software rendering | about 1 fps |
| 720x1280, 4 vCPU | about 7 fps |
| 720x1280, 8 vCPU, SwiftShader | about 12 fps |
| 720x1280, 8 vCPU, Mesa llvmpipe through Xvfb with `-gpu host` | about 12 fps, the same |
| `-gpu guest` | falls back to lavapipe, no faster |
| `swangle_indirect` | crashes when the map app starts |

- With `swiftshader_indirect`, opening the toll-payment app's authorize screen from the map app's "sign in with" link
  killed the emulator 4 times out of 4, within about 0.2 s, with nothing in its log (once `bad color buffer handle`).
  gdb showed the cause: a SIGSEGV in `RenderThread` at an address in SwiftShader's JIT heap, that is in shader code
  SwiftShader generated. `-feature -Vulkan` changed nothing.
- On a 16 vCPU, 32 GB instance with emulator 37.2.13, tapping two of the map app's tabs also killed the emulator after
  two idle minutes. More CPU or memory did not help. Emulator 37.3.9 from the beta channel
  (`sdkmanager --channel=1 --install emulator`) fixed that tab crash, but not the sign-in crash.
- An Android 16 image crashed the same way under SwiftShader, and once went black with Play open.
- What works: `Xvfb :99 -screen 0 1280x1024x24 +extension GLX`, Mesa (`xvfb mesa-utils libgl1-mesa-dri libegl-mesa0`),
  then `DISPLAY=:99 emulator ... -gpu host`; `glxinfo -B` shows `llvmpipe`. The sign-in then completes ("Signed in
  successfully"), at the same 12 fps.
- Inferred: `-gpu lavapipe` or `software` is the documented way to reach Mesa without Xvfb; not tried. No reference
  sets Mesa's `LP_NUM_THREADS` (default: one thread per core, [envvars](https://docs.mesa3d.org/envvars.html)); capping
  it so the guest's vCPUs and the encoder keep theirs is untried. The 12 fps is consistent with serve-emu's "~20fps"
  software ceiling with the in-guest H.264 encoder taking the same vCPUs.

Other flags the references share: `-no-window -no-boot-anim -no-audio` (Orca, Devin, Google, HQarroum), animation
scales set to 0 after boot (budtmo `emulator.py` L253-257, T3 `scripts/mobile-showcase.ts` L1250-1418), 4 GB and 4
cores in Google's AVD (`emu/templates/avd/MediumPhone.avd/config.ini` L11-36). The official page has no `-cores`
(`hw.cpu.ncore` in the AVD) and lists `-scale` as deprecated.

**State across restarts.** Sign-ins live in `userdata-qemu.img` and survive restarts; `-wipe-data` removes them;
loading a named snapshot overwrites user data with the snapshot's
([command line](https://developer.android.com/studio/run/emulator-commandline)). Inferred from that line: loading an
old snapshot reverts a later sign-in. Orca and serve-emu use the default Quick Boot.

## 3. Google's Android CLI, as tried

- Installed from `https://dl.google.com/android/cli/latest/linux_x86_64/install.sh` into `~/.local/bin`; no checksum is
  published. Version 1.1.16919734. The SDK is set in `~/.androidrc` as `--sdk=$HOME/android`. Every call was given
  `--no-metrics`. The pages read do not state a licence (not verified further).
- `android screen capture --annotate --output=F` numbered the map app's controls by vision, 37 on its home screen, in
  2.4 s, though the app is Flutter. `android screen resolve --screenshot=F --string="input tap #10"` turned a number
  into coordinates.
- `android layout --flat` returned the hierarchy; the first call installs a layout server on the device.
- `android input text "Phở Hà Nội" "clz=android.widget.EditText"` typed Vietnamese correctly, without ADBKeyBoard
  (OpenMausBot and Android-MCP need ADBKeyBoard for non-ASCII; Orca types US-ASCII only).
- From another machine: it ignores `ADB_SERVER_SOCKET` but honours `ANDROID_ADB_SERVER_PORT`, so a tunnel to the
  device host's adb server plus that variable works.
- Google's page says "The `emulator` tool is deprecated" in favour of `android emulator`
  ([command line](https://developer.android.com/studio/run/emulator-commandline)); the Windows emulator command is
  disabled (search summary, not verified).

## 4. Streaming the screen to a web page

| Option | Transport | Setup | Input | Licence |
|---|---|---|---|---|
| ws-scrcpy (NetrisTV, 2.6k stars) | scrcpy fork, H.264 over WebSocket; MSE, WASM or WebCodecs decoders | Node server with node-gyp; any adb device | Multi-touch and keys over scrcpy control | MIT (`README.md` L1-80) |
| Google WebRTC (emulator gRPC `Rtc` + android-emulator-webrtc React) | WebRTC media from the emulator itself; a Python gateway signals; TURN (`-turncfg`) across NAT | Emulator started with `-grpc`; emulator only | gRPC | Apache-2.0 (`README.md` L236-262, `launch-emulator.sh` L189) |
| noVNC + x11vnc (budtmo) | VNC of the X display holding the emulator window | Xvfb, a window, x11vnc, websockify | Mouse as touch | noVNC MPL-2.0, x11vnc GPL-2.0 (from memory, not verified); budtmo's own licence adds telemetry amendments |
| Own WebSocket around scrcpy-server (Orca; T3 through serve-emu) | scrcpy H.264, decoded with WebCodecs; serve-emu adds WebRTC, an MSE fallback and keyframe replay for late joiners | A pinned scrcpy-server jar pushed to the device; Orca's client is about 300 lines | scrcpy control socket; Orca falls back to `adb shell input` | scrcpy Apache-2.0, serve-emu Apache-2.0, hub MIT |

serve-emu caps the stream at 1280 because "The emulator has no hardware video encoder; its software H.264 encoder
(c2.android.avc.encoder) only sustains 60fps below roughly a megapixel"; its other source, the emulator's gRPC
screenshot API with libx264 on the host, moves encoding out of the guest (`src/scrcpy.ts` L140-150, `src/cli.ts`
L208-215; the hub's default is `grpc-screenshot`, `options.ts` L17-19). Orca uses the same 1280 and scrcpy's default
8 Mbps (`backends/android-emulator-backend.ts` L91). T3's hub binds loopback and the server proxies an allowlist of
routes with a cookie or a short-lived `wsTicket` (`DeviceHubProxy.ts` L23-51). During the trials the owner watched
with desktop scrcpy over an SSH tunnel to the instance's adb server.

## 5. Flutter apps

- Flutter builds its Android accessibility tree only when accessibility is on (`AccessibilityBridge.java` L393-413,
  L543-544; [dart_mcp_server README](https://github.com/dart-lang/ai/tree/main/pkgs/dart_mcp_server)).
- uiautomator does see a Flutter `TextField`, with empty text, because Flutter 3.7+ puts the label in the hint, which
  the dumper omits ([mobile-mcp#340](https://github.com/mobile-next/mobile-mcp/issues/340)); icon-only buttons have no
  label and fields can be missing until typed ([mobile-mcp#127](https://github.com/mobile-next/mobile-mcp/issues/127)).
  On the owner's machine uiautomator showed no nodes for the map app. Inferred: an app that is mostly a map or
  `CustomPaint` shows almost nothing, whatever the tool.
- Since Flutter 3.19, `Semantics(identifier:)` becomes the Android `resource-id`; keys are not exposed
  ([appium-flutter-driver README](https://github.com/appium/appium-flutter-driver) L32-39,
  [Maestro](https://docs.maestro.dev/get-started/supported-platform/flutter)). This is the app team's change to make.
- Reading the Dart VM service (mobile-mcp, appium-flutter-driver, the Dart MCP server) needs a debug or profile build
  or a driver extension, so it does not reach an app installed from Play.
- So for such apps a screenshot is what the agent has. The references that work without a tree are screenshot-driven
  (Journeys, Agent Mode, Firebase's agent, Devin, mobilerun `--vision-only`), and a screenshot with numbered boxes
  turns "tap the third icon" into "tap #10" with coordinates the tool computes, which the Android CLI did on this app.
  Orca gives its agent no Android screenshot at all, which leaves it blind on these apps (inferred).

## 6. The references' common default, per decision

| Decision | Common default | From |
|---|---|---|
| Where the device runs | On a machine the server reaches: its own, or one named host; an AWS Intel instance with nested virtualization works | T3 Code (`devices.md` L117-145), Orca (`orca serve` or SSH host, guide L32-34); AWS from the trials |
| Emulator or phone | Emulator for testing; a physical phone only for a personal phone's privacy, or leased from a vendor | T3 Code, Orca, Devin; OpenMausBot (physical); BrowserStack, Device Farm |
| Device image | An AVD created beforehand and booted on demand, waiting for `sys.boot_completed`; or an image with the SDK and AVD baked in | Orca (`android-avd-boot.ts` L99-134), T3 Code, serve-emu; Devin, Google's containers |
| Keeping sign-ins | Default Quick Boot and a kept `userdata-qemu.img`; no wipe | Orca, serve-emu, `expo-device-hub/packages/@expo/hub-android-utils/README.md` L83; Google's containers wipe instead, for tests |
| Rendering without a GPU | Mesa llvmpipe: `-gpu host` on Xvfb with GLX worked; `swiftshader_indirect` is deprecated and crashed | Trials; [acceleration](https://developer.android.com/studio/run/emulator-acceleration); Google `run-with-gpu.sh` (Xvfb with `-gpu host`). `-gpu lavapipe` without Xvfb: inferred, untried |
| Boot and speed flags | `-no-window -no-boot-anim -no-audio`; animations 0; 720x1280; 8 vCPU for about 12 fps | Orca, Devin, Google, HQarroum; budtmo, T3; trials (Devin: "lower-resolution device profile helps") |
| What the agent sees | Structure and a screenshot together; an annotated screenshot when the tree is empty | OpenMausBot, T3/agent-device, mobile-mcp, Android-MCP, Android CLI `layout` + `screen capture --annotate`; Journeys, mobilerun |
| How the agent acts | `adb shell input`, or the scrcpy control socket when a stream is open; non-ASCII through Android CLI `input text` | Orca (`android-device-input.ts` L27-85), OpenMausBot, Agent Mode; Android CLI from the trials |
| Agent tool surface | A few MCP tools plus a pinned CLI the agent calls (T3), or the tools themselves (OpenMausBot); Android CLI is the official one | T3 (`tools.ts` L30-80), OpenMausBot (`phone-proxy.ts` L262-274), Android CLI |
| Person watching in the web | scrcpy H.264 over a WebSocket, WebCodecs, max size 1280, 8 Mbps | Orca, T3/serve-emu, ws-scrcpy; WebRTC optional (serve-emu, Google) |
| Exposure | Device tools on loopback; the server proxies an allowlist with short-lived tickets | T3 (`DeviceHubProxy.ts` L23-51), serve-emu |
| Who holds the device | One holder at a time, released explicitly | OpenMausBot (claim, 409), T3 (session per thread), Orca (`kill`, guide L99) |
| A person taking over | No reference gates it; a gate would come from the browser design, not from an Android reference | Inferred; issue #193 |
| Signing in to accounts | The person types passwords through the stream; the agent never does | OpenMausBot skill (`skills/phone-harness/SKILL.md` L12-33); the owner's practice in the trials |
| Tool versions | Pinned, installed by the server | T3 (`deviceToolVersions.json`), Orca (scrcpy-server 2.4); the Android CLI publishes no checksum, so pinning it needs a recorded hash of our own (inferred) |
