// imageMeta reads an image's width, height and size without a library, which zca-js needs to send an image
// (Options.imageMetadataGetter). Adapted from the Hermes Zalo plugin's imageMeta
// (https://github.com/hitechcloudvn/hermes-zalo-plugin, sidecar/server.js, MIT License, Copyright (c) 2026
// HiTechCloud.VN); changed: WebP is read too, and an image it cannot read is sent with no size rather than a made-up
// one.
import { readFile, stat } from "node:fs/promises";

export function imageSize(buf) {
  if (buf.length > 24 && buf[0] === 0x89 && buf[1] === 0x50) {
    return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20) };
  }

  if (buf.length > 4 && buf[0] === 0xff && buf[1] === 0xd8) {
    let off = 2;
    while (off + 9 < buf.length) {
      if (buf[off] !== 0xff) {
        off++;
        continue;
      }

      const marker = buf[off + 1];
      if (marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc) {
        return { width: buf.readUInt16BE(off + 7), height: buf.readUInt16BE(off + 5) };
      }

      off += 2 + buf.readUInt16BE(off + 2);
    }
  }

  if (buf.length > 10 && buf[0] === 0x47 && buf[1] === 0x49) {
    return { width: buf.readUInt16LE(6), height: buf.readUInt16LE(8) };
  }

  // WebP (RIFF....WEBP), which zca-js sends as an image: its three chunk kinds, as Google's container spec lays
  // them out (developers.google.com/speed/webp/docs/riff_container). Not in the Hermes plugin's.
  if (buf.length > 30 && buf.toString("ascii", 0, 4) === "RIFF" && buf.toString("ascii", 8, 12) === "WEBP") {
    const chunk = buf.toString("ascii", 12, 16);
    if (chunk === "VP8X") return { width: 1 + buf.readUIntLE(24, 3), height: 1 + buf.readUIntLE(27, 3) };
    if (chunk === "VP8L") {
      const b = buf.readUInt32LE(21);

      return { width: 1 + (b & 0x3fff), height: 1 + ((b >> 14) & 0x3fff) };
    }
    if (chunk === "VP8 ") return { width: buf.readUInt16LE(26) & 0x3fff, height: buf.readUInt16LE(28) & 0x3fff };
  }

  return null;
}

export async function imageMeta(path) {
  try {
    const size = imageSize(await readFile(path));
    if (!size) return null;

    return { ...size, size: (await stat(path)).size };
  } catch {
    return null;
  }
}
