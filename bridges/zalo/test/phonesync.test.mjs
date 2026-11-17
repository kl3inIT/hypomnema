import assert from "node:assert/strict";
import crypto from "node:crypto";
import { test } from "node:test";
import zlib from "node:zlib";

import { backupKey, decode, frame, keys, syncEvents } from "../src/phonesync.mjs";

// The phone encrypts the backup's key to the bridge's public key (RSA PKCS#1 v1.5); the AES key is the first 32
// upper-case hex characters of what it decrypts to (softtyos deriveZdbAesKey).
test("the backup's key is decrypted with the pair the bridge made", () => {
  const k = keys();
  const raw = crypto.randomBytes(32);
  const pub = crypto.createPublicKey({ key: Buffer.from(k.publicKey, "base64"), format: "der", type: "spki" });
  const encrypted = crypto.publicEncrypt({ key: pub, padding: crypto.constants.RSA_PKCS1_PADDING }, raw);

  const key = backupKey(k.privateKey, encrypted.toString("base64"));
  assert.equal(key.length, 32);
  assert.equal(key.toString("ascii"), raw.toString("hex").toUpperCase().slice(0, 32));
});

// A cmd 601 frame of the listener whose control is the phone's answer, sealed with the socket's cipher key as Zalo
// seals events (zca-js decodeEventData, encrypt 2: AES-GCM, then deflated).
test("the phone's answer is read off a sealed cmd 601 frame", () => {
  const cipherKey = crypto.randomBytes(16);
  const body = { data: { controls: [
    { content: { act_type: "syncmsgmb", act: "user_confirm", data: JSON.stringify({ user_action: 1, public_key: "pk" }) } },
    { content: { act_type: "group", act: "join" } },
  ] } };

  const iv = crypto.randomBytes(16);
  const aad = crypto.randomBytes(16);
  const c = crypto.createCipheriv("aes-128-gcm", cipherKey, iv);
  c.setAAD(aad);
  const sealed = Buffer.concat([c.update(zlib.deflateSync(Buffer.from(JSON.stringify(body)))), c.final(), c.getAuthTag()]);
  const payload = { encrypt: 2, data: encodeURIComponent(Buffer.concat([iv, aad, sealed]).toString("base64")) };

  const header = Buffer.from([1, 0, 0, 0]);
  header.writeUInt16LE(601, 1);
  const f = frame(Buffer.concat([header, Buffer.from(JSON.stringify(payload))]));
  assert.equal(f.cmd, 601);

  const events = syncEvents(decode(f.body, cipherKey.toString("base64")));
  assert.deepEqual(events, [{ act: "user_confirm", data: { user_action: 1, public_key: "pk" } }]);
});

test("a frame that is not JSON, or a control of another kind, is not a sync event", () => {
  assert.equal(frame(Buffer.from([1, 0, 0, 0, 0x7b])), null);
  assert.deepEqual(syncEvents({ data: { controls: [{ content: { act_type: "transfer_msg", act: "other" } }] } }), []);
});
