import { describe, expect, it } from "vitest"

import type { MeetingDetail, MeetingLine } from "@/api/generated/types.gen"

import { captureSupport } from "./meeting-capture"
import { withLine } from "./meeting-session"

function detail(): MeetingDetail {
  return {
    meeting: {
      id: "mtg_1",
      title: "Meeting",
      titled: false,
      people: [],
      file_name: "recording.wav",
      media_type: "audio/wav",
      size: 0,
      duration_ms: 0,
      state: "recording",
      source: "live",
      kind: "online",
      created_at: "2026-11-18T09:00:00Z",
      notes: { state: "waiting", summary: "", sections: [] },
      speakers: [],
      actions: 0,
      open_actions: 0,
    },
    lines: [],
    items: [],
    suggestions: [],
  }
}

function line(n: number, track: MeetingLine["track"], startMs: number): MeetingLine {
  return {
    n,
    track,
    start_ms: startMs,
    end_ms: startMs + 500,
    speaker: "1",
    heard: "1",
    text: `line ${n}`,
  }
}

describe("a meeting recorded live", () => {
  // A line kept is added by when it was said, not by its number: the tab's line said first reads first though the
  // microphone's was kept first; its voice joins the meeting the first time it speaks, the microphone online as You;
  // a line already there is not added twice (issue #820).
  it("adds the lines kept in the order they were said", () => {
    let d = withLine(detail(), line(1, "mic", 3_000))
    d = withLine(d, line(2, "tab", 1_000))
    d = withLine(d, line(2, "tab", 1_000))

    expect(d.lines.map((l) => l.n)).toEqual([2, 1])
    expect(d.meeting.speakers.map((s) => [s.track, s.label, s.name, s.owner])).toEqual([
      ["mic", "1", "You", true],
      ["tab", "1", "Speaker 1", false],
    ])
  })

  // Chromium on a computer shares a tab's sound; a phone, or another browser, records the microphone only (Nojoin's
  // featureDetect, MemoryOS's captureSupport).
  it("offers a tab's sound only where it can be shared", () => {
    const media = { getUserMedia: () => undefined, getDisplayMedia: () => undefined }
    const desktop =
      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
    const phone =
      "Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Mobile Safari/537.36"
    const firefox = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
    const nav = (userAgent: string) =>
      ({ userAgent, mediaDevices: media }) as unknown as Parameters<typeof captureSupport>[0]

    expect(captureSupport(nav(desktop))).toBe("sharedAudio")
    expect(captureSupport(nav(phone))).toBe("microphoneOnly")
    expect(captureSupport(nav(firefox))).toBe("microphoneOnly")
  })
})
