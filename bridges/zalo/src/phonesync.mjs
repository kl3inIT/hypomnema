// The phone's store (issue #697, step 4 of #613): Zalo PC's "Sync messages", ported from softtyos/zca-js (MIT,
// https://github.com/softtyos/zca-js; its notice is in third_party/softtyos-zca-js): its pullMobile and
// cancelPullMobile (src/apis/sync/), its RSA key and the decryption of the backup's key (src/crypto/zdb-cipher.ts),
// and the sync events it reads off the listener's cmd 601 controls (src/realtime/handlers/control.handler.ts,
// src/models/SyncEvent.ts). zca-js has neither: the requests go through its api.custom, and the controls are read
// off its socket with its own frame header and decodeEventData (src/apis/listen.ts, src/utils.ts, MIT), ported here.
import crypto from "node:crypto";
import { createWriteStream } from "node:fs";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";
import zlib from "node:zlib";

// The control's acts the sync is told by (softtyos initializeSyncEvent).
const ACTS = new Set(["user_confirm", "syncmsg_info", "transfer_error"]);

// keys makes the RSA-2048 pair the phone encrypts the backup's key to (softtyos generateRSAKeyPair): the public
// key as SPKI DER in base64, the private as PEM.
export function keys() {
  const { publicKey, privateKey } = crypto.generateKeyPairSync("rsa", {
    modulusLength: 2048,
    publicKeyEncoding: { type: "spki", format: "der" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
  });

  return { publicKey: publicKey.toString("base64"), privateKey };
}

// backupKey is the AES-256 key of the backup: its encrypted_key decrypted with RSA PKCS#1 v1.5, of which the first
// 32 upper-case hex characters, as ASCII (softtyos deriveZdbAesKey). Node refuses PKCS#1 v1.5 decryption where
// OpenSSL was hardened against CVE-2023-46809; softtyos then decrypts with the key's CRT parts, as here.
export function backupKey(privateKey, encryptedBase64) {
  const cipher = Buffer.from(encryptedBase64, "base64");
  let raw;
  try {
    raw = crypto.privateDecrypt({ key: privateKey, padding: crypto.constants.RSA_PKCS1_PADDING }, cipher);
  } catch {
    raw = pkcs1Decrypt(privateKey, cipher);
  }

  const hex = raw.toString("hex").toUpperCase();
  if (hex.length < 32) throw new Error("the backup's key is too short");

  return Buffer.from(hex.slice(0, 32), "ascii");
}

const big = (b64url) => BigInt("0x" + Buffer.from(b64url, "base64url").toString("hex"));

function modPow(base, exp, mod) {
  let result = 1n;
  base %= mod;
  while (exp > 0n) {
    if (exp & 1n) result = (result * base) % mod;
    base = (base * base) % mod;
    exp >>= 1n;
  }

  return result;
}

// pkcs1Decrypt is softtyos's rsaDecryptPkcs1Manual.
function pkcs1Decrypt(privateKey, cipher) {
  const k = crypto.createPrivateKey(privateKey).export({ format: "jwk" });
  const [n, p, q, dp, dq, qi] = [k.n, k.p, k.q, k.dp, k.dq, k.qi].map(big);
  const c = BigInt("0x" + cipher.toString("hex"));
  const m1 = modPow(c, dp, p);
  const m2 = modPow(c, dq, q);
  const h = (qi * (((m1 - m2) % p) + p)) % p;
  const m = m2 + h * q;
  const size = Math.ceil(n.toString(16).length / 2);
  const em = Buffer.from(m.toString(16).padStart(size * 2, "0"), "hex");
  if (em[0] !== 0 || em[1] !== 2) throw new Error("the backup's key has no PKCS#1 v1.5 padding");
  const sep = em.indexOf(0, 2);
  if (sep < 10) throw new Error("the backup's key has no PKCS#1 v1.5 separator");

  return em.subarray(sep + 1);
}

// frame reads one of the listener's frames: its header (version, cmd, subCmd) and its JSON body (zca-js
// listen.ts, getHeader and onmessage).
export function frame(data) {
  if (!Buffer.isBuffer(data) || data.length < 4) return null;
  try {
    return { version: data[0], cmd: data.readUInt16LE(1), subCmd: data[3], body: JSON.parse(data.subarray(4).toString("utf8")) };
  } catch {
    return null;
  }
}

// decode is zca-js's decodeEventData: plain JSON (0), base64 and deflated (1), or AES-GCM with the listener's
// cipher key, then deflated (2) or not (3).
export function decode(body, cipherKey) {
  const type = body?.encrypt;
  if (typeof body?.data !== "string" || typeof type !== "number") return null;
  if (type === 0) return JSON.parse(body.data);

  let buf = Buffer.from(type === 1 ? body.data : decodeURIComponent(body.data), "base64");
  if (type !== 1) {
    if (!cipherKey || buf.length < 48) return null;
    const key = Buffer.from(cipherKey, "base64");
    const decipher = crypto.createDecipheriv(`aes-${key.length * 8}-gcm`, key, buf.subarray(0, 16));
    decipher.setAAD(buf.subarray(16, 32));
    const sealed = buf.subarray(32);
    decipher.setAuthTag(sealed.subarray(sealed.length - 16));
    buf = Buffer.concat([decipher.update(sealed.subarray(0, sealed.length - 16)), decipher.final()]);
  }

  return JSON.parse((type === 3 ? buf : zlib.inflateSync(buf)).toString("utf8"));
}

// syncEvents are the sync's controls of one decoded cmd 601 body: [{act, data}] (softtyos control.handler.ts).
export function syncEvents(decoded) {
  const out = [];
  for (const control of decoded?.data?.controls ?? decoded?.controls ?? []) {
    const content = control?.content ?? {};
    if (content.act_type !== "syncmsgmb" && content.act_type !== "transfer_msg") continue;
    if (!ACTS.has(content.act)) continue;
    const data = typeof content.data === "string" ? JSON.parse(content.data) : content.data;
    out.push({ act: content.act, data });
  }

  return out;
}

// call sends one of the sync's requests with zca-js's encrypted params (softtyos pullMobile, cancelPullMobile).
async function call(api, path, payload) {
  const name = `hypomnema_${path}`;
  if (typeof api[name] !== "function") {
    api.custom(name, async ({ ctx, utils, props }) => {
      const params = utils.encodeAES(JSON.stringify({ ...props, imei: ctx.imei }));
      if (!params) throw new Error("the request's params could not be encrypted");
      const url = utils.makeURL(`${ctx.zpwServiceMap.file[0]}/api/message/${path}`,
        { zpw_ver: ctx.API_VERSION, zpw_type: ctx.API_TYPE, params, nretry: 0 }, false);

      return utils.resolve(await utils.request(url, { method: "GET" }));
    });
  }

  return api[name](payload);
}

// pull asks the phone for its store (pull_mobile_msg, cmd 12000): the phone shows the owner a confirmation.
export const pull = (api, publicKey) =>
  call(api, "pull_mobile_msg", { pc_name: "Hypomnema", public_key: publicKey, from_seq_id: 0, is_retry: 0,
    min_seq_id: 0, temp_key: "" });

// cancel withdraws a request the phone did not answer (cancel_pull_mobile_msg).
export const cancel = (api, publicKey) =>
  call(api, "cancel_pull_mobile_msg", { pc_name: "Hypomnema", public_key: publicKey });

// download writes the backup the phone uploaded to path, with the session's cookies (softtyos getSyncData).
export async function download(api, url, path) {
  const name = "hypomnema_download_backup";
  if (typeof api[name] !== "function") {
    api.custom(name, async ({ utils, props }) => {
      const res = await utils.request(props.url, { method: "GET" }, true);
      if (!res.ok || !res.body) throw new Error(`the backup could not be downloaded: HTTP ${res.status}`);
      await pipeline(Readable.fromWeb(res.body), createWriteStream(props.path, { mode: 0o600 }));

      return {};
    });
  }

  return api[name]({ url, path });
}

// noised turns the phone's ids of people (fids) and groups (gids), plain numbers, into the ids Zalo Web uses
// (softtyos getNoisedIds, /api/znoise, with its fallback): an id it does not know answers "".
export async function noised(api, fids, gids) {
  const name = "hypomnema_noised";
  if (typeof api[name] !== "function") {
    api.custom(name, async ({ ctx, utils, props }) => {
      const params = utils.encodeAES(JSON.stringify({ fids: props.fids.map(Number), gids: props.gids.map(Number) }));
      if (!params) throw new Error("the request's params could not be encrypted");
      let data;
      try {
        data = await utils.resolve(await utils.request(utils.makeURL(`${ctx.zpwServiceMap.profile[0]}/api/znoise`, { params }), { method: "GET" }));
      } catch (err) {
        try {
          data = await utils.resolve(await utils.request(utils.makeURL("https://zwid.api.zalo.me/api/znoise"),
            { method: "POST", body: new URLSearchParams({ params }).toString() }));
        } catch {
          throw err;
        }
      }

      const list = (raw, n, group) => Array.from({ length: n }, (_, i) => {
        let v = raw?.[i] == null ? "" : String(raw[i]);
        if (group && v.startsWith("g")) v = v.slice(1);
        return v === "0" ? "" : v;
      });

      return { fids: list(data?.fids, props.fids.length, false), gids: list(data?.gids, props.gids.length, true) };
    });
  }

  return api[name]({ fids, gids });
}
