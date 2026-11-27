import assert from "node:assert/strict";
import { test } from "node:test";

import { imageSize } from "../src/imagemeta.mjs";
import { closed, failure, media, members, mentions, message, older, parseRequest, rejected } from "../src/protocol.mjs";

test("a line that is no request is said so, and a request keeps its id", () => {
  assert.equal(parseRequest("{").error, "not JSON");
  assert.equal(parseRequest('{"method":"groups"}').error, "a request is {id, method, params}");
  assert.deepEqual(parseRequest('{"id":7,"method":"groups"}').request, { id: 7, method: "groups", params: {} });
});

test("a login Zalo refuses is answered with the code rejected; a network failure is not", () => {
  assert.deepEqual(JSON.parse(failure(3, rejected(new Error("Đăng nhập thất bại")))),
    { id: 3, error: "Đăng nhập thất bại", code: "rejected" });
  assert.deepEqual(JSON.parse(failure(4, rejected(new Error("fetch failed")))), { id: 4, error: "fetch failed" });
});

test("a socket another Zalo Web took over is told as replaced; another close as lost", () => {
  assert.equal(closed(3000, "duplicate").replaced, true);
  assert.equal(closed(3003, "kicked").replaced, true);
  assert.equal(closed(1006, "").replaced, false);
});

// zca-js's GroupMessage and UserMessage (src/models/Message.ts): a group message from someone else and the owner's
// own message in a direct chat.
test("a message is kept as the control plane reads it", () => {
  const group = message({
    type: 1,
    threadId: "g1",
    isSelf: false,
    data: { msgId: "m1", cliMsgId: "c1", actionId: "77", uidFrom: "u2", dName: "Lan", ts: "1700000000000",
      msgType: "webchat", content: "Báo giá đâu anh?", quote: { globalMsgId: 99 } },
  });
  assert.deepEqual(group, {
    id: "m1", cliId: "c1", actionId: "77", chat: "g1", group: true, from: "u2", fromName: "Lan", fromMe: false,
    at: 1700000000000, kind: "webchat", text: "Báo giá đâu anh?", media: null, quoteId: "99",
  });

  const mine = message({ type: 0, threadId: "u3", isSelf: true, data: { msgId: "m2", uidFrom: "me", content: "Ok" } });
  assert.equal(mine.group, false);
  assert.equal(mine.fromMe, true);
});

// The content shapes the Hermes Zalo plugin's parseContent and zaloclaw read: a photo with its caption, a file, a
// voice note, a link, a sticker and a call.
test("a message's content is read as its text and its file", () => {
  assert.deepEqual(media("chat.photo", { title: "Ảnh hiện trường", href: "https://f/p.jpg", params: '{"hd":"https://f/hd.jpg"}' }),
    { text: "Ảnh hiện trường", media: { type: "image", url: "https://f/hd.jpg", fileName: "", size: 0 } });
  assert.deepEqual(media("share.file", { title: "bao-gia.pdf", href: "https://f/x", params: '{"fileSize":"2048"}' }),
    { text: "", media: { type: "document", url: "https://f/x", fileName: "bao-gia.pdf", size: 2048 } });
  assert.equal(media("chat.voice", { href: "https://f/v.m4a" }).media.type, "audio");
  assert.deepEqual(media("share.file", { fileName: "note.mp3", fileUrl: "https://f/n.mp3", totalSize: 9 }).media,
    { type: "audio", url: "https://f/n.mp3", fileName: "note.mp3", size: 9 });
  assert.deepEqual(media("chat.recommended", { title: "Xem bài này", href: "https://vnexpress.net/a" }),
    { text: "Xem bài này\nhttps://vnexpress.net/a", media: null });
  assert.deepEqual(media("chat.sticker", { id: 1 }).media, { type: "sticker", url: "", fileName: "", size: 0 });
  assert.deepEqual(media("chat.recommended", { action: "recommened.calltime", title: "Cuộc gọi thoại" }),
    { text: "Cuộc gọi thoại", media: null });
});

test("the next page of the direct chats' queue starts at the oldest actionId", () => {
  assert.equal(older(["7300000000000000123", "730000000000000099", "x", ""]), "730000000000000099");
  assert.equal(older([]), "");
});

// zca-js needs an image's size to send it: a PNG's and a GIF's headers say it (the Hermes Zalo plugin's imageMeta).
test("an image's size is read from its header", () => {
  const png = Buffer.alloc(32);
  png.set([0x89, 0x50, 0x4e, 0x47]);
  png.writeUInt32BE(640, 16);
  png.writeUInt32BE(480, 20);
  assert.deepEqual(imageSize(png), { width: 640, height: 480 });

  const gif = Buffer.from([0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x20, 0x00, 0x10, 0x00, 0x00]);
  assert.deepEqual(imageSize(gif), { width: 32, height: 16 });
  const webp = Buffer.alloc(32);
  webp.write("RIFF", 0, "ascii");
  webp.write("WEBP", 8, "ascii");
  webp.write("VP8X", 12, "ascii");
  webp.writeUIntLE(799, 24, 3);
  webp.writeUIntLE(599, 27, 3);
  assert.deepEqual(imageSize(webp), { width: 800, height: 600 });
  assert.equal(imageSize(Buffer.from("not an image")), null);
});

// zca-js's getGroupInfo (src/apis/getGroupInfo.ts): a group names its members in memberIds, in memVerList with
// their versions, and in currentMems; each is one member (issue #1163).
test("a group's members are every id Zalo names them by, once each", () => {
  assert.deepEqual(
    members({
      name: "Nhóm dự án",
      memberIds: ["200", " 300 "],
      memVerList: ["200_3", "400_0"],
      currentMems: [{ id: "500", dName: "Hoa" }, { id: "" }, null],
    }),
    { name: "Nhóm dự án", ids: ["200", "300", "400", "500"] },
  );
  assert.deepEqual(members(undefined), { name: "", ids: [] });
});

// zca-js's Mention (src/apis/sendMessage.ts): {pos, uid, len}; one that is not whole is not sent (issue #1200).
test("a group's mentions go to zca-js as {pos, uid, len}, and one that is not whole is left out", () => {
  assert.deepEqual(mentions([{ pos: 0, uid: "u1", len: 8 }, { pos: 12, uid: "", len: 4 }, { pos: -1, uid: "u2", len: 3 },
    { pos: 3, uid: "u3", len: 0 }, null]), [{ pos: 0, uid: "u1", len: 8 }]);
  assert.deepEqual(mentions(undefined), []);
});
