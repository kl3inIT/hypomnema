# Goldens of the frame and the backgrounds

The PNGs here are what Chromium draws, and `golden_test.go` holds the Go raster (`plate.go`, `background.go`) to
them (issue #908, PR 2, check 2): a mean difference of at most 3/255 a channel, and at most 2 % of pixels off by more
than 24/255, both pictures over magenta so the hole is compared too. For a frame, the pixels it draws on the ground
are also held on their own to a mean of 1.5/255: on so dark a ground a missing shadow moves the whole picture's
mean by 1/255 only.

- `frame-<chrome>[-safe]-<aspect>.png`: aidemo's frame HTML (https://github.com/tandryukha/aidemo at commit
  b67235a, `src/frame.ts:128-190`, MIT, Copyright (c) 2026 Andrii Taran), fed our layout's rectangles in video
  pixels at a device scale of 1, on the solid ground `#0e1322` (aidemo's padColor). Chromium's screenshot keeps the
  hole transparent.
- `background-<preset>-<aspect>.png`, `background-colors-<aspect>.png`: autodemo's `background()`
  (https://github.com/mdayan8/autodemo at commit 73e5408, `src/compositor.html:21-29`, `:53-77`, MIT, Copyright (c)
  2026 Md Ayan), copied as it is, on a canvas a quarter of the video's size (Chrome dithers its gradients, so a full
  size golden is a megabyte; the drawing scales with the canvas).
- `spec.json`: the layouts and backgrounds they are drawn from. `TestTheGoldensAreOfTheseLayouts` fails when the
  layouts change, so the goldens are drawn again.

`render.mjs` carries the two references' MIT notices.

## Drawing them again

From the repository's root, with the web client's packages and Playwright's Chromium installed (`AGENTS.md`):

```sh
go test ./internal/controlplane/videos/compose -run TestTheGoldensAreOfTheseLayouts -update
cd web && node ../internal/controlplane/videos/compose/testdata/frames/render.mjs
```

The goldens in the repository were drawn by Chromium 158.0.8127.18 (Playwright 1.65.0) on linux/amd64.
