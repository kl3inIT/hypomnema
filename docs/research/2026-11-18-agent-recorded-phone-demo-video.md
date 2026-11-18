# An agent records a narrated demo video of an Android app

Date: 2026-11-18. Asked by the owner after the Muse work (#782): agents should film the VMAP app on the node's phone
and turn the take into a short narrated, captioned video (the "product kit" videos), with the owner approving the
result. Clones are in `.tmp/screenrec/` (git-ignored); paths below are inside it. Earlier video-studio references
(moonreel, MoneyPrinterTurbo, OpenMontage) are in [2026-11-17-muse-and-growth-studio.md](2026-11-17-muse-and-growth-studio.md) §4.

## 1. Capturing an Android screen

| Reference | How | Limit | Where |
|---|---|---|---|
| scrcpy 3.x (Apache-2.0, a60891a) | The client muxes the server's H.264 packets into mp4/mkv with libavformat: the first config packet becomes extradata, each packet's duration is next PTS − its PTS, PTS rebased to the first packet and kept increasing, later config packets dropped; variable frame rate (a frame only when the screen changes). Headless: `scrcpy --no-playback --no-window --record=file.mkv` | none; `--time-limit` optional | `app/src/recorder.c:65-391`, `doc/recording.md:64-93` |
| scrcpy server stream | 12-byte header per packet: 8 bytes PTS in µs from MediaCodec with session/config/key flags, 4 bytes size | `raw_stream=true` drops the PTS | `server/.../Streamer.java:17-19, 85-123`, `app/src/demuxer.c:56-112`, `doc/develop.md:445-468` |
| Appium Android driver | `adb screenrecord` in 180 s pieces, restarted until the asked length, merged with ffmpeg | 30 min total | `appium-android-driver/lib/commands/recordscreen.js:22-23, 104, 224-257` |
| Maestro | `adb screenrecord --bit-rate 100000`, `--time-limit 0` on API ≥ 34, SIGINT to stop, pull the file; `maestro record` overlays the test's status board | 180 s below API 34 | `Maestro/maestro-client/.../AndroidDriver.kt:507-545`, `maestro-cli/.../RecordCommand.kt:157-199` |

Hypomnema already runs scrcpy-server 3.3.4 on a node that holds the phone and reads its packets with their PTS
(`internal/node/device/scrcpy.go:59,144-192`); one stream fans out to every view (`view.go:13-22`, after serve-emu).
A recording can be one more reader of that stream, with the packets' own device clock.

## 2. Agent-made demo videos

| Reference (all MIT) | Capture | How the agent's steps meet the timeline | Replay script | Post-production | Review |
|---|---|---|---|---|---|
| autodemo (73e5408) | CDP frames / ScreenCaptureKit 60 fps (`native/capture.swift:87-95`) | live `camera_say{text, click:[x,y], chapter}` just before each action, stamped on the session clock minus pauses (`src/mcp.js:270-321`, `src/live.js:106,154-161`) | scenes JSON (`src/mcp.js:40-50`) | Kokoro TTS; long takes "are fine" (no cap, `src/mcp.js:287`); lines pushed so they never overlap (`live.js:225-234`); idle fast-forward (`render.js:17-37`); zoom + cursor ripple at the click; canvas → ffmpeg, 9:16 | none |
| aidemo (b67235a, most complete) | Playwright recordVideo, ffmpeg, OBS, simctl, Maestro | storyboard + jobs `record/voice/captions/compose/qa/probe` (`src/mcp/server.ts:967-1330`) | storyboard (`src/types.ts:626-669, 988-1062`), resume from a scene, reuse by content hash | fit voice to video: idle cut to 400 ms, `setpts` retime (`MIN_FACTOR` 0.5, `MAX_STRETCH` 1.6, `:80-81`), else freeze or slow push-in (`src/compose.ts:76-89`); Whisper word captions (`src/captions.ts:105-190`); zoompan, 9:16 | agent's own `lint/probe/qa` |
| voila (MIT) | CDP JPEG frames → 30 fps | one `voila_record(url, steps_yaml)` (`mcp.js:43-158`) | recipe in `recipe.json` and in the MP4's metadata (`pipeline.js:16-44`) | voice first, each step held for its clip + 600 ms (`tour.js:234-247`) | `voila_review` sampled frames |
| demotape (MIT) | AVFoundation 30 fps + 60 Hz event log | JSON `scenes:[{say, steps, expect}]`; reveal/commit split with a configurable action lead (`tools/demo-driver/driver.mjs:922-975`; `expect` fail-fast `:958-962`) | same JSON; `expect` stops the take at the first failure | Whisper words (`Captions.swift:321`), 9:16 reframe, silence cut with pitch kept | vision model checks each scene, retries; exit 3 = a person must look (`DemoVerifier.swift`) |
| agent-demo-video (MIT) | Playwright screencast → 30 fps | actions logged with ms offsets (`src/capture.ts:36-45,356`) | Markdown `### SHOT` blocks (`src/parse-script.ts:25-60`) | ElevenLabs timestamps / faster-whisper words; video stretched to the voice, freeze last frame (`src/timeline.ts:29-36`); 9:16 | a separate reviewer agent on a contact sheet (`skills/demo-video/SKILL.md:40-58`) |
| screencli (MIT, core on a paid server) | Playwright WebM | live `narrate(text)` + clicks stamped on wall-clock time (`recording/event-log.ts:10,23`) | none | zoom, cursor, idle gaps > 3 s cut, 9:16 | none |

Not open source: the "Screenplay" recorder (no public repository). Agents that drive Android without recording:
scrcpy-with-webui-and-ai, GhostThumb, ghost-in-the-droid/android-agent.

## 3. What none of them does

- Record an **Android phone on a Linux server**: autodemo and demotape film macOS/Windows windows, the rest a
  browser; aidemo reaches phones only through iOS simctl and Maestro.
- A **person's approval** before the video is used: they stop at the agent's or a model's own check.
- Vietnamese voices and captions.

## 4. Recommendation

1. Record from the node's existing scrcpy stream, stamping every step on the stream's PTS (scrcpy `recorder.c`;
   avoids screencli's wall-clock drift).
2. Live step tools in autodemo's shape (`start`, `say{text, tap}`, `stop`), the agent still acting with the
   `device_*` tools.
3. Save the take as a scenes script with `expect` (demotape, voila) so a new app build can be filmed again.
4. Voice first, then fit (voila, aidemo `compose.ts`): idle cut, retime within limits, freeze; Whisper word
   captions, 9:16 in ffmpeg (aidemo, demotape); Vietnamese voices as MoneyPrinterTurbo uses.
5. A vision check per scene (demotape, agent-demo-video), then the owner approves (owner, 2026-11-18).

Split into three issues: recording + step tools; voice, compose and captions; replay and checks.

## Review of the first design (2026-11-18)

A Codex review of the first take design (issue #817) found that a take cannot use the view's dropping queue, must merge config as scrcpy's demuxer does, needs a session clock rather than the last PTS (still screens send nothing), must count the still tail, needs a lease over several calls, a transfer path of its own, and an access contract. It also pointed to fMP4 through `bluenviron/mediacommon` as MediaMTX records live H.264 (`mediamtx/internal/recorder/format_fmp4*.go`, 6d41178; mediacommon 75fa748), which plays in browsers. The design was revised on the issue.

## Sources

Clones (shallow, 2026-11-18): Genymobile/scrcpy a60891a, mobile-dev-inc/Maestro 4413a33,
appium/appium-android-driver, appium/appium-uiautomator2-driver 3cdb2e9, appium/appium-adb ba7ad47,
mdayan8/autodemo 73e5408, tandryukha/aidemo b67235a, halleylabs/voila, gabosarmiento/demotape,
OrionArchitekton/agent-demo-video, screencli. Search leads (not evidence): GitHub topic `product-demo`,
screencli's 2026 comparison post.
