# A browser take: filming a tab of the shared browser for the Studio (issue #1189)

Studio filmed only the node's Android phone (issue #817). Issue #1189 adds a take of an agent's tab of the shared
browser (`internal/controlplane/browser`), made into a video by the same Make pipeline (issue #818). The owner
approved the direction on 2026-11-26 after the comparison below, which is the issue's, kept here as written.

## How the references record a browser

The coordinator read them in code (clones in its `.tmp/screenrec/`). For the build, autodemo `mdayan8/autodemo` at
`73e5408` and agent-demo-video `OrionArchitekton/agent-demo-video` at `f63b61a` were cloned and read again (the
paths below), and agent-browser `vercel-labs/agent-browser` at `v0.42.0` for its `get box` and `set viewport`
(`cli/src/mcp.rs:1203-1208, 3299-3306, 3404-3417`, `cli/src/native/actions.rs:8542-8562, 9229-9246`,
`cli/src/native/browser.rs:2181-2230`).

| Reference | Capture | Timeline | Encode | Events |
|---|---|---|---|---|
| autodemo (MIT, 73e5408) | raw CDP `Page.startScreencast`, JPEG q90, DPR 2 (`src/perform.js:213-224`, `src/browser.js:7`) | latest frame at or before each output time (`src/render.js:193-230`) | x264 medium CRF 16, 30 fps (`render.js:250`) | typed event log with box/point (`perform.js:31,175`) |
| agent-demo-video (MIT, f63b61a) | Playwright `page.screencast` (CDP), JPEG q90 (`src/capture.ts:285`) | each frame held until the next by CDP timestamp, last until stop (`src/screencast.ts` frameDurations) | x264 veryfast CRF 18, `fps=30` (`src/ffmpeg.ts:14-18,99-111`) | `events_<shot>.json` `{kind,tMs,box}` (`capture.ts:27-45`) |
| demotape (MIT) | raw CDP screencast JPEG q80, chose it because Playwright's VP8 WebM was unusable (`tools/demo-driver/frames-capture.mjs`) | constant-rate grid, hold last image (`FrameSequence.swift:91-115`) | H.264 | events sidecar |
| Maestro web (Apache-2.0) | CDP screencast JPEG q80 via Selenium (`WebScreenRecorder.kt`) | per-frame durations from arrival (`JcodecVideoEncoder.kt`) | H.264 VFR | none |
| aidemo, screencli (MIT) | Playwright `recordVideo` (VP8 WebM); aidemo's own code calls it "variable frame pacing and softer rendering" (`aidemo/src/capture.ts:10-11`) | left to Playwright | x264 | event logs |

4 of 6 capture CDP screencast frames; none emulates a mobile device (no `isMobile`/touch): a phone-sized viewport
only.

## The approved design, and what was built

| # | Decision | From | Built |
|---|---|---|---|
| 1 | CDP `Page.startScreencast` on the shared browser's own connection: JPEG 90, device scale factor 2, every frame, each acked | autodemo `perform.js:213-224`, `browser.js:7`; agent-demo-video q90 | As said, but at the viewport's size: see "Device scale factor 2" below |
| 2 | Each frame until the next by its CDP timestamp, the last until stop; 30 fps H.264 (libx264 CRF 18, yuv420p, even sides) | agent-demo-video `screencast.ts`, `ffmpeg.ts`; demotape | Ported into `internal/controlplane/browsertake/timeline.go`; the first frame from the take's start (autodemo `render.js:193-230`), the output cut to the take's length, TV range |
| 3 | A take like a phone take, in `/takes`, its source recorded | Hypomnema's take model (#817) | `source: phone|browser`, one segment `seg-000.mp4` |
| 4 | Tools mirror the phone's; reuse `video_say`/`video_stop` if the take id routes them, else a `browser_video_start` | agent-demo-video, autodemo | `browser_video_start`, `browser_video_say`, `browser_video_stop`: the take id does not route `video_*`, which a worker's tools server sends to the phone's routes and the phone's switch (off in the full preset) names (`devicetool.Routed`, `agenttool.ToolFamilies`) |
| 5 | The ring: the clicked element's box, scaled to the video, as a phone tap | autodemo, agent-demo-video event boxes | The box is agent-browser's `get box` (`cli/src/native/element.rs`, `get_element_bounding_box`), its centre the step's tap |
| 6 | A viewport only, no mobile emulation | all six | As said |
| 7 | A phone take's bounds; the browser's own context | existing take limits | 400 MB free disk, the work's end, the tab's end; the agent's own tab and the gate |

## Device scale factor 2: not possible with Chrome's screencast here

Tried on 2026-11-26 on a worker with agent-browser 0.42.0 (the pinned release, SHA-256 `b69e0f5e…`) driving
Playwright's Chromium 158.0.8127.18 through the real `browser.Engine`, a page of a button and an input, a
390×844 viewport:

- With agent-browser's `agent_browser_set_viewport {width: 390, height: 844, scale: 2}` the page says
  `devicePixelRatio` 2 and `Page.captureScreenshot` is 780×1688, but every screencast frame is 390×844, with
  `maxWidth` 780 or 1560.
- The same on the film's own session (`Emulation.setDeviceMetricsOverride` with `deviceScaleFactor` 2), with a
  window of 780×1688 (`Browser.setContentsSize`), and on the headless shell (`chrome-headless-shell`, which
  Playwright runs headless) with a raw CDP client: still 390×844. Chromium sizes a screencast frame by the view in
  CSS pixels and never scales it up.
- `Emulation.setDeviceMetricsOverride {width: 390, height: 844, deviceScaleFactor: 2, scale: 2,
  dontSetVisibleSize: true}` on a 780×1688 window gave 780×1688 frames of a 390-wide layout, sharp, but
  agent-browser's click at the element's centre then missed it (the page did not change), as the view is
  scaled under its input; it would also change the owner's view of the tab.

So a take is filmed at the viewport's size, as agent-demo-video films (its screencast's size is the viewport,
`src/capture.ts:285`). A sharper take of a phone layout would need another capture, `Page.captureScreenshot` at a
scale, which no reference does for a recording; left out.

## Checked

- `internal/controlplane/browser/film_unix_test.go`: the screencast's commands on a fake Chrome, the frames'
  acks, a frame under the owner's hold not kept, the viewport set and given back by agent-browser (a stand-in),
  `get box`.
- `internal/controlplane/browsertake/recorder_test.go`, with the pinned ffmpeg: a click and a typing give a take of
  H.264 at 30 fps, the viewport's size, 150 frames for 5 s, each frame on screen from its time, the steps' times
  and taps; a still page of 5 s gives a 5 s take.
- `internal/controlplane/videos/browsertake_test.go`, with the pinned ffmpeg: such a take made into a video with
  voice (AAC), captions and a ring at each element's centre.
- By hand, with the real engine as above: frames arrive with Chrome's timestamps (the first 30-40 ms after the
  start), `get box` gives `{100, 200, 120, 40}` for a button placed there, and a click and a fill show in the
  frames.
