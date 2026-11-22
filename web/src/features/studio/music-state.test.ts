import { afterEach, describe, expect, test, vi } from "vitest"

import type { VideoMusicTrack } from "@/api/generated/types.gen"

import { amount, goneOf, refusalOf, trackName, uploadTrack } from "./music-state"

const track: VideoMusicTrack = {
  id: "mus_0123456789abcdef",
  name: "Lo-fi",
  media_type: "audio/mpeg",
  length_ms: 95_000,
  bytes: 1_500_000,
  created_at: "2026-11-22T10:00:00Z",
}

const json = (body: unknown, status: number) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

afterEach(() => vi.restoreAllMocks())

// Scenario: the owner adds a file from the Music group. It is sent to the upload route outside the contract (issue
// #910, row 6a): its bytes as they are, as application/octet-stream, named by the file; the track comes back.
describe("a track's upload", () => {
  test("sends the file as it is, named by the file without its extension", async () => {
    const sent: Request[] = []
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      sent.push((input as Request).clone())

      return json(track, 201)
    })
    const file = new File([new Uint8Array([1, 2, 3, 4])], "Lo-fi.mp3", { type: "audio/mpeg" })

    await expect(uploadTrack(file)).resolves.toEqual(track)
    const request = sent[0]!
    const url = new URL(request.url)
    expect(request.method).toBe("POST")
    expect(url.pathname).toBe("/api/v1/videos/music")
    expect(url.searchParams.get("name")).toBe("Lo-fi")
    expect(request.headers.get("Content-Type")).toBe("application/octet-stream")
    expect(new Uint8Array(await request.arrayBuffer())).toEqual(new Uint8Array([1, 2, 3, 4]))
  })

  test("a refusal says the server's reason", async () => {
    const reason = "the library holds 4990 MB of its 5000 MB; this track is 12 MB"
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      json({ code: "insufficient_storage", message: reason }, 507),
    )
    const file = new File([new Uint8Array(8)], "Big.flac")

    await expect(uploadTrack(file)).rejects.toMatchObject({ status: 507, message: reason })
  })
})

// Scenario: the route refuses before it reads the file: no length (411), too big (413), another upload (429), no
// room (507). The server's words are shown; a refusal without them, as a proxy's page, says the route's limit.
describe("the line of a refused upload", () => {
  test.each([
    [411, "a track's upload needs its Content-Length"],
    [413, "a track is at most 64 MB"],
    [429, "another track is being uploaded"],
    [507, "the control plane has 300 MB of disk free; the track needs 412 MB"],
  ])("%i in the server's words", (status, message) => {
    expect(refusalOf(status, { code: "x", message }).message).toBe(message)
  })

  test.each([
    [411, /without its length/],
    [413, /at most 64 MB/],
    [429, /Another track is being uploaded/],
    [507, /no room for this track/],
  ])("%i from a page that is not the server's", (status, line) => {
    expect(refusalOf(status, "<html><body>nginx</body></html>").message).toMatch(line)
  })
})

describe("a track's name", () => {
  test("is the file's, without its extension", () => {
    expect(trackName("Nhạc nền.final.m4a")).toBe("Nhạc nền.final")
    expect(trackName("  intro.wav")).toBe("intro")
    expect(trackName(".ogg")).toBe(".ogg")
  })

  test("is at most 200 characters, as the server takes", () => {
    expect(Array.from(trackName(`${"ạ".repeat(250)}.mp3`))).toHaveLength(200)
  })
})

// Scenario: a look names a track the owner deleted since (row 16). The Music group keeps it in the list as deleted,
// with the name the look kept; a track the library has, or none, is not.
describe("a track gone from the library", () => {
  test("is the look's track the library has no more, with the name it kept", () => {
    const stored = { track: "mus_00000000000000d1", name: "Old", length_ms: 30_000, gain_db: 0 }

    expect(goneOf("mus_00000000000000d1", [track], stored)).toEqual({
      track: "mus_00000000000000d1",
      name: "Old",
      length_ms: 30_000,
    })
  })

  test("is not a track the library has, none, or a library not read yet", () => {
    expect(goneOf(track.id, [track], undefined)).toBeUndefined()
    expect(goneOf("none", [], undefined)).toBeUndefined()
    expect(goneOf("mus_00000000000000d1", undefined, undefined)).toBeUndefined()
  })
})

describe("what the library holds", () => {
  test("is said in the quota's decimal units, never rounded up to full", () => {
    expect(amount(61_300_000)).toBe("61.3 MB")
    expect(amount(4_995_000_000)).toBe("4.99 GB")
    expect(amount(5_000_000_000)).toBe("5 GB")
  })
})
