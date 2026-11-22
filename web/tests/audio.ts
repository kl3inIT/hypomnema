// Test audio for the Studio's player (issue #910, rows 18 and 19), written here rather than kept in git: the Web job
// has no ffmpeg, so each format is built by hand from its specification, each a second long or about.
// - wav: RIFF PCM, 16-bit mono 44.1 kHz, a 440 Hz tone.
// - mp3: MPEG-1 Layer III frames at 128 kb/s, 44.1 kHz mono, whose side information says no data: each decodes to
//   silence (ISO/IEC 11172-3, 2.4.1.7: part2_3_length 0).
// - flac: STREAMINFO and frames of one VERBATIM subframe of the tone (xiph.org/flac/format.html), with their CRC-8
//   and CRC-16.
// - m4a: AAC-LC in MP4, frames of silence (below).
// - ogg: Opus in Ogg (RFC 7845): OpusHead, OpusTags, then 20 ms CELT packets of silence (TOC 0xF8 and 0xFF 0xFE,
//   the silence frame WebRTC sends), with the pages' CRC-32.

const rate = 44_100

function tone(samples: number): Int16Array {
  const out = new Int16Array(samples)
  for (let i = 0; i < samples; i++)
    out[i] = Math.round(0.3 * 32767 * Math.sin((2 * Math.PI * 440 * i) / rate))

  return out
}

export function wav(): Buffer {
  const pcm = tone(rate)
  const data = Buffer.alloc(pcm.length * 2)
  pcm.forEach((s, i) => data.writeInt16LE(s, i * 2))
  const head = Buffer.alloc(44)
  head.write("RIFF", 0)
  head.writeUInt32LE(36 + data.length, 4)
  head.write("WAVEfmt ", 8)
  head.writeUInt32LE(16, 16)
  head.writeUInt16LE(1, 20)
  head.writeUInt16LE(1, 22)
  head.writeUInt32LE(rate, 24)
  head.writeUInt32LE(rate * 2, 28)
  head.writeUInt16LE(2, 32)
  head.writeUInt16LE(16, 34)
  head.write("data", 36)
  head.writeUInt32LE(data.length, 40)

  return Buffer.concat([head, data])
}

// mp3 is seconds of silence, one by default.
export function mp3(seconds = 1): Buffer {
  // 144 × 128000 / 44100 bytes a frame, without padding; 39 frames are about a second.
  const frame = Buffer.alloc(417)
  frame.set([0xff, 0xfb, 0x90, 0xc0])

  return Buffer.concat(Array.from({ length: 39 * seconds }, () => frame))
}

function crc8(bytes: Uint8Array): number {
  let c = 0
  for (const b of bytes) {
    c ^= b
    for (let k = 0; k < 8; k++) c = c & 0x80 ? ((c << 1) ^ 0x07) & 0xff : (c << 1) & 0xff
  }

  return c
}

function crc16(bytes: Uint8Array): number {
  let c = 0
  for (const b of bytes) {
    c ^= b << 8
    for (let k = 0; k < 8; k++) c = c & 0x8000 ? ((c << 1) ^ 0x8005) & 0xffff : (c << 1) & 0xffff
  }

  return c
}

export function flac(): Buffer {
  const block = 4096
  const frames = 11
  const pcm = tone(block * frames)
  const info = Buffer.alloc(34)
  info.writeUInt16BE(block, 0)
  info.writeUInt16BE(block, 2)
  // Sample rate (20 bits), channels − 1 (3), bits a sample − 1 (5), then the samples (36): 44100, 0, 15.
  const total = BigInt(pcm.length)
  const packed = (BigInt(rate) << 44n) | (0n << 41n) | (15n << 36n) | total
  info.writeBigUInt64BE(packed, 10)
  const out: Buffer[] = [Buffer.from("fLaC"), Buffer.from([0x80, 0, 0, 34]), info]

  for (let f = 0; f < frames; f++) {
    // Sync and fixed blocking; 4096 samples (12) at 44.1 kHz (9); mono, 16 bits; the frame's number.
    const head = Buffer.from([0xff, 0xf8, 0xc9, 0x08, f])
    const body = Buffer.alloc(1 + block * 2)
    body[0] = 0x02 // VERBATIM, no wasted bits.
    for (let i = 0; i < block; i++) body.writeInt16BE(pcm[f * block + i]!, 1 + i * 2)
    const frame = Buffer.concat([head, Buffer.from([crc8(head)]), body])
    const end = Buffer.alloc(2)
    end.writeUInt16BE(crc16(frame))
    out.push(frame, end)
  }

  return Buffer.concat(out)
}

function crc32(bytes: Uint8Array): number {
  let c = 0
  for (const b of bytes) {
    c ^= b << 24
    for (let k = 0; k < 8; k++) c = c & 0x80000000 ? (c << 1) ^ 0x04c11db7 : c << 1
  }

  return c >>> 0
}

function page(flag: number, granule: bigint, seq: number, packets: Buffer[]): Buffer {
  const segments = packets.flatMap((p) => {
    const lacing = Array.from({ length: Math.floor(p.length / 255) }, () => 255)

    return [...lacing, p.length % 255]
  })
  const head = Buffer.alloc(27 + segments.length)
  head.write("OggS", 0)
  head[5] = flag
  head.writeBigUInt64LE(granule, 6)
  head.writeUInt32LE(0x1d1, 14)
  head.writeUInt32LE(seq, 18)
  head[26] = segments.length
  head.set(segments, 27)
  const out = Buffer.concat([head, ...packets])
  out.writeUInt32LE(crc32(out), 22)

  return out
}

export function ogg(): Buffer {
  const preSkip = 312
  const opusHead = Buffer.alloc(19)
  opusHead.write("OpusHead", 0)
  opusHead[8] = 1
  opusHead[9] = 1
  opusHead.writeUInt16LE(preSkip, 10)
  opusHead.writeUInt32LE(48_000, 12)
  const vendor = Buffer.from("hypomnema test")
  const tags = Buffer.alloc(8 + 4 + vendor.length + 4)
  tags.write("OpusTags", 0)
  tags.writeUInt32LE(vendor.length, 8)
  vendor.copy(tags, 12)
  const packets = Array.from({ length: 50 }, () => Buffer.from([0xf8, 0xff, 0xfe]))

  return Buffer.concat([
    page(0x02, 0n, 0, [opusHead]),
    page(0x00, 0n, 1, [tags]),
    page(0x04, BigInt(preSkip + packets.length * 960), 2, packets),
  ])
}

// box is an ISO BMFF box (ISO/IEC 14496-12, 4.2): its size, its type, then its body.
function box(type: string, ...body: Buffer[]): Buffer {
  const head = Buffer.alloc(8)
  head.writeUInt32BE(8 + body.reduce((n, b) => n + b.length, 0), 0)
  head.write(type, 4, "latin1")

  return Buffer.concat([head, ...body])
}

// full is a full box: a box whose body starts with its version and flags (ISO/IEC 14496-12, 4.2).
const full = (type: string, flags: number, ...body: Buffer[]) => {
  const head = Buffer.alloc(4)
  head.writeUInt32BE(flags)

  return box(type, head, ...body)
}

const u32 = (...values: number[]) => {
  const out = Buffer.alloc(values.length * 4)
  values.forEach((v, i) => out.writeUInt32BE(v, i * 4))

  return out
}

// descriptor is an MPEG-4 descriptor (ISO/IEC 14496-1, 8.3.3): its tag, its size in one byte, then its body.
const descriptor = (tag: number, ...body: Buffer[]) => {
  const inner = Buffer.concat(body)

  return Buffer.concat([Buffer.from([tag, inner.length]), inner])
}

// The unity matrix of a movie and a track header (ISO/IEC 14496-12, 8.2.2 and 8.3.2).
const unity = u32(0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000)

// m4a is AAC-LC in MP4, the m4a the server takes (videos/music.go, sniffMusic: AAC in an mp4 brand): an 'M4A '
// file of one mono 44.1 kHz track whose 1024-sample frames are each a single channel element of no spectral data,
// so silence (ISO/IEC 14496-3, 4.4.2: global_gain 160, max_sfb 0, then ID_END), about three seconds of it. The
// AudioSpecificConfig 0x12 0x08 says AAC-LC (2), 44.1 kHz (index 4), mono (1).
export function m4a(): Buffer {
  const frames = 130
  const frame = Buffer.from([0x01, 0x40, 0x20, 0x07])
  const length = frames * 1024
  const ftyp = box("ftyp", Buffer.from("M4A "), u32(0), Buffer.from("M4A mp42isom"))
  const esds = full(
    "esds",
    0,
    descriptor(
      0x03,
      Buffer.from([0, 0, 0]),
      descriptor(
        0x04,
        // MPEG-4 audio (0x40), an audio stream (5 << 2 | 1), its buffer and bit rates.
        Buffer.from([0x40, 0x15, 0, 0, 0]),
        u32(8000, 8000),
        descriptor(0x05, Buffer.from([0x12, 0x08])),
      ),
      descriptor(0x06, Buffer.from([0x02])),
    ),
  )
  const mp4aHead = Buffer.alloc(28)
  mp4aHead.writeUInt16BE(1, 6) // data_reference_index
  mp4aHead.writeUInt16BE(1, 16) // channels
  mp4aHead.writeUInt16BE(16, 18) // sample size
  mp4aHead.writeUInt32BE(rate * 0x10000, 24)
  const mvhdTail = Buffer.alloc(80)
  mvhdTail.writeUInt32BE(0x10000, 0) // rate 1.0
  mvhdTail.writeUInt16BE(0x100, 4) // volume 1.0
  unity.copy(mvhdTail, 16)
  mvhdTail.writeUInt32BE(2, 76) // next track ID
  const tkhdTail = Buffer.alloc(60)
  tkhdTail.writeUInt16BE(0x100, 12) // volume 1.0
  unity.copy(tkhdTail, 16)
  const mdhdTail = Buffer.from([0x55, 0xc4, 0, 0]) // language "und"
  const hdlr = full("hdlr", 0, u32(0), Buffer.from("soun"), u32(0, 0, 0), Buffer.from("Sound\0"))

  const moov = (offset: number) =>
    box(
      "moov",
      full("mvhd", 0, u32(0, 0, 1000, Math.round((length * 1000) / rate)), mvhdTail),
      box(
        "trak",
        full("tkhd", 7, u32(0, 0, 1, 0, Math.round((length * 1000) / rate), 0, 0), tkhdTail),
        box(
          "mdia",
          full("mdhd", 0, u32(0, 0, rate, length), mdhdTail),
          hdlr,
          box(
            "minf",
            full("smhd", 0, u32(0)),
            box("dinf", full("dref", 0, u32(1), full("url ", 1))),
            box(
              "stbl",
              full("stsd", 0, u32(1), box("mp4a", mp4aHead, esds)),
              full("stts", 0, u32(1, frames, 1024)),
              full("stsc", 0, u32(1, 1, frames, 1)),
              full("stsz", 0, u32(frame.length, frames)),
              full("stco", 0, u32(1, offset)),
            ),
          ),
        ),
      ),
    )
  // The one chunk of samples starts after the moov, whose size does not depend on where it points.
  const offset = ftyp.length + moov(0).length + 8

  return Buffer.concat([
    ftyp,
    moov(offset),
    box("mdat", ...Array.from({ length: frames }, () => frame)),
  ])
}
