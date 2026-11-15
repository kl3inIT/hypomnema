import { describe, expect, it } from "vitest"

import { parsePane } from "@/features/workbench/pane"

import {
  avcCodecString,
  keyInput,
  parseSemuPacket,
  scanAccessUnit,
  screenPoint,
  socketUrl,
  textInputs,
} from "./stream"

const hex = (s: string) => Uint8Array.from(s.match(/../g) ?? [], (b) => parseInt(b, 16))

describe("a frame of the phone's view", () => {
  // serve-emu's golden SEMU v1 bytes (LLP 0008): a key frame at 42 µs, then its access unit.
  it("reads the SEMU header and leaves the access unit", () => {
    const raw = hex("53454d5501010000000000000000002a" + "0000000165")
    const packet = parseSemuPacket(raw.buffer)

    expect(packet.isKey).toBe(true)
    expect([...packet.data]).toEqual([0, 0, 0, 1, 0x65])
  })

  it("takes a message without the header as the access unit itself", () => {
    const packet = parseSemuPacket(hex("0000000141").buffer)

    expect(packet.isKey).toBeNull()
    expect(packet.data.length).toBe(5)
  })

  // An SPS (Orca's x264 fixture), a PPS and an IDR slice: a key frame that can start a decoder, of its profile.
  it("finds the SPS and the IDR picture of a key frame, and its codec", () => {
    const unit = hex("0000000167640032acd94044" + "0000000168ebe3cb22c0" + "0000000165888400")
    const scanned = scanAccessUnit(unit)

    expect(scanned.isKey).toBe(true)
    expect(scanned.sps).not.toBeNull()
    expect(avcCodecString(scanned.sps!)).toBe("avc1.640032")
    expect(scanAccessUnit(hex("000001419a")).isKey).toBe(false)
  })
})

describe("the owner's input", () => {
  const key = (key: string, more: Partial<{ ctrlKey: boolean; metaKey: boolean }> = {}) =>
    keyInput({ key, ctrlKey: false, metaKey: false, altKey: false, ...more })

  // T3 Code's keys: Escape is Back, the editing keys their Android codes, a typed character its text.
  it("is a key code, a text, or Back", () => {
    expect(key("Escape")).toEqual({ type: "back" })
    expect(key("Enter")).toEqual({ type: "key", keycode: 66 })
    expect(key("Backspace")).toEqual({ type: "key", keycode: 67 })
    expect(key("ư")).toEqual({ type: "text", text: "ư" })
    expect(key("c", { ctrlKey: true })).toBeNull()
    expect(key("Shift")).toBeNull()
  })

  // A text message is at most 300 UTF-8 bytes, split between characters.
  it("splits a pasted text between characters", () => {
    expect(textInputs("mật khẩu")).toEqual([{ type: "text", text: "mật khẩu" }])

    const long = textInputs("ạ".repeat(150))
    expect(long.map((m) => (m.type === "text" ? m.text.length : 0))).toEqual([100, 50])
  })

  // A 720×1280 screen in a 1000×800 picture is 450 wide and centred: a point is from 0 to 1 of the screen.
  it("maps a pointer through the letterbox to the screen", () => {
    const rect = { left: 0, top: 0, width: 1000, height: 800 }
    const screen = { width: 720, height: 1280 }

    expect(screenPoint(rect, screen, 500, 400, false)).toEqual({ x: 0.5, y: 0.5 })
    expect(screenPoint(rect, screen, 275, 0, false)).toEqual({ x: 0, y: 0 })
    expect(screenPoint(rect, screen, 100, 400, false)).toBeNull()
    expect(screenPoint(rect, screen, 100, 400, true)).toEqual({ x: 0, y: 0.5 })
  })
})

describe("the phone's place", () => {
  it("opens its WebSocket on the page's own server", () => {
    expect(socketUrl({ protocol: "https:", host: "cp.example" }, "t 1")).toBe(
      "wss://cp.example/api/v1/device/views/t%201",
    )
  })

  it("is a tab a pane keeps", () => {
    expect(parsePane({ tabs: [{ kind: "phone" }], active: "phone" })).toEqual({
      tabs: [{ kind: "phone" }],
      active: "phone",
    })
  })
})
