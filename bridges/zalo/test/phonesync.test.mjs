import assert from "node:assert/strict";
import crypto from "node:crypto";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import zlib from "node:zlib";

import { backupKey, decode, download, frame, keys, noised, pull, step, syncEvents } from "../src/phonesync.mjs";

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

// zca-js keeps its service map on the API, not on the context its custom APIs are given (zca-js src/apis.ts,
// src/apis/custom.ts): a request built from the context failed on zm with "reading 'file'" (issue #703).
function fakeApi() {
  const asked = [];
  const api = {
    zpwServiceMap: { file: ["https://file.example"], profile: ["https://profile.example"] },
    custom(name, callback) {
      api[name] = (props) =>
        callback({
          ctx: { imei: "i1", API_VERSION: 1, API_TYPE: 30 },
          props,
          utils: {
            encodeAES: (s) => Buffer.from(s).toString("base64"),
            makeURL: (base, params = {}) => `${base}?${new URLSearchParams(params)}`,
            request: async (url) => {
              asked.push(url);

              return {};
            },
            resolve: async () => ({ fids: ["w7"], gids: ["g55"] }),
          },
        });
    },
  };

  return { api, asked };
}

test("the phone is asked at Zalo's file service, and ids are turned at its profile service", async () => {
  const { api, asked } = fakeApi();

  await pull(api, "pk");
  assert.match(asked[0], /^https:\/\/file\.example\/api\/message\/pull_mobile_msg\?/);

  assert.deepEqual(await noised(api, ["7"], ["55"]), { fids: ["w7"], gids: ["55"] });
  assert.match(asked[1], /^https:\/\/profile\.example\/api\/znoise\?/);
});

// Zalo may gzip an event's payload, which zca-js's pako.inflate detects; inflating it as zlib only threw, and the
// phone's answer was dropped (issue #715). Read as deplao-builder reads it.
test("a gzipped event's payload is read", () => {
  const body = { data: { controls: [
    { content: { act_type: "syncmsgmb", act: "syncmsg_info", data: { public_key: "pk", url: "u" } } },
  ] } };
  const payload = { encrypt: 1, data: zlib.gzipSync(Buffer.from(JSON.stringify(body))).toString("base64") };

  assert.deepEqual(syncEvents(decode(payload)), [{ act: "syncmsg_info", data: { public_key: "pk", url: "u" } }]);
});

// What each of the phone's events means (deplao-builder MobileSyncService.onSyncEvent): preparing and paused are
// waits, not failures; error_code 0 is none.
test("the phone's events are read as deplao-builder reads them", () => {
  const cases = [
    ["user_confirm", { user_action: 1 }, "wait"],
    ["user_confirm", { user_action: 3 }, "wait"],
    ["user_confirm", { user_action: 0 }, "refused"],
    ["user_confirm", { user_action: 2 }, "failed"],
    ["transfer_error", { status: 1, error_code: 0 }, "wait"],
    ["transfer_error", { status: 2 }, "wait"],
    ["transfer_error", { error_code: 0 }, "none"],
    ["transfer_error", { error_code: -5, error_msg: "full" }, "failed"],
    ["syncmsg_info", {}, "ready"],
  ];
  for (const [act, data, want] of cases) assert.equal(step(act, data).next, want, `${act} ${JSON.stringify(data)}`);
});

// zca-js's raw request adds no headers: the backup is asked for with Zalo Web's User-Agent and the session's
// cookie for chat.zalo.me, then without the cookie after a 4xx, as deplao-builder does (issue #726).
test("the backup is downloaded with the session's cookie, then without it after a 4xx", async () => {
  const asked = [];
  const api = {
    custom(name, callback) {
      api[name] = (props) =>
        callback({
          props,
          ctx: {
            userAgent: "UA",
            cookie: { getCookieString: async (url) => (url === "https://chat.zalo.me/" ? "zpw_sek=s" : "") },
          },
          utils: {
            request: async (url, options) => {
              asked.push(options.headers);

              return asked.length === 1
                ? new Response(JSON.stringify({ error_message: "bad cookie" }), { status: 400 })
                : new Response("backup bytes", { status: 200 });
            },
          },
        });
    },
  };

  const path = join(mkdtempSync(join(tmpdir(), "zalo-dl-")), "b.db.crypt");
  await download(api, "https://f.zdn.vn/backup.db.crypt", path);

  assert.equal(asked[0]["User-Agent"], "UA");
  assert.equal(asked[0].Cookie, "zpw_sek=s");
  assert.equal(asked[1].Cookie, undefined);
  assert.equal(readFileSync(path, "utf8"), "backup bytes");
});
