import assert from "node:assert/strict";
import { test } from "node:test";

import { closed, failure, message, parseRequest, rejected } from "../src/protocol.mjs";

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

// zca-js's GroupMessage and UserMessage (src/models/Message.ts): a group message from someone else, the owner's own
// message in a direct chat, and a file shared as an attachment.
test("a message is kept as the control plane reads it", () => {
  const group = message({
    type: 1,
    threadId: "g1",
    isSelf: false,
    data: { msgId: "m1", cliMsgId: "c1", uidFrom: "u2", dName: "Lan", ts: "1700000000000", msgType: "webchat",
      content: "Báo giá đâu anh?", quote: { globalMsgId: 99 } },
  });
  assert.deepEqual(group, {
    id: "m1", cliId: "c1", chat: "g1", group: true, from: "u2", fromName: "Lan", fromMe: false, at: 1700000000000,
    kind: "webchat", text: "Báo giá đâu anh?", attachment: null, quoteId: "99",
  });

  const mine = message({ type: 0, threadId: "u3", isSelf: true, data: { msgId: "m2", uidFrom: "me", content: "Ok" } });
  assert.equal(mine.group, false);
  assert.equal(mine.fromMe, true);

  const file = message({
    type: 1,
    threadId: "g1",
    data: { msgId: "m3", msgType: "share.file",
      content: { title: "bao-gia.pdf", description: "", href: "https://f.zalo/x", thumb: "" } },
  });
  assert.equal(file.text, "bao-gia.pdf");
  assert.deepEqual(file.attachment, { url: "https://f.zalo/x", title: "bao-gia.pdf", thumb: "" });
});
