// Issue #1186, plan row 3: the triage keeps an item's notes as what is needed and posts every later update as a
// comment. This runs the prompt, triage.md, with a real model against triage-world.mjs, the Bot's tools as an MCP
// server (triage-run.mjs), so it runs only when HYPOMNEMA_TEST_CLAUDE names Claude Code
// (HYPOMNEMA_TEST_CLAUDE=claude node --test deploy/thu-ky/triage.test.mjs), as the node's real Claude Code tests do;
// CI, which has no model, skips it.
import assert from "node:assert/strict";
import { test } from "node:test";

import { runTriage } from "./triage-run.mjs";

const claude = process.env.HYPOMNEMA_TEST_CLAUDE;

const chat = "84912000111@s.whatsapp.net";
const item = {
  id: "tdo_dwryeciip544shejwudmyn5csa",
  revision: 4,
  title: "Báo anh Trung Huynh (Blaze) khi có cập nhật về TTS (đang tạm gác)",
  notes: "## Source\nWhatsApp riêng anh Trung, 26/11: anh hứa báo lại khi các sếp mở lại TTS (3EB0E056F0533EEF55A863).\n\n" +
    "## Plan\n1. Khi TTS được mở lại, test các voice và báo anh Trung.",
  done_when: "Đã báo anh Trung cập nhật về TTS.",
  state: "open",
  why_open: "promised",
  assignee: { kind: "me" },
  priority: 3,
  priority_reason: "Đã hứa báo lại, chưa có hạn",
  source: { kind: "whatsapp", id: chat, account: "main", quote: "Khi nào có update gì thêm em báo anh nhé ạ.",
    ref: { kind: "whatsapp_message", ref: "3EB0E056F0533EEF55A863" } },
  created_by: { kind: "bot", id: "bot_main" },
};
const message = (ID, At, SenderName, FromMe, Text) => ({ Chat: chat, ChatName: "Trung Huynh", ID, At, SenderName,
  FromMe, Text });
const messages = [
  message("AC760E5BB1B73109326B2558B7211E78", "2026-11-26T02:05:00Z", "Trung Huynh", false,
    "Em ơi bên em có process tiếp vụ TTS không?"),
  message("3EB0E056F0533EEF55A863", "2026-11-26T07:38:00Z", "Phan Đạt", true,
    "TTS đang bị các sếp tạm gác để ưu tiên việc khác ạ. Khi nào có update gì thêm em báo anh nhé ạ."),
  message("AC7C8C756650E103A854BF4F1A34D461", "2026-11-26T07:40:00Z", "Trung Huynh", false, "Ok thank em nhé!"),
  // The new message: more on the same thing, which asks nothing and changes neither what is needed nor its plan.
  message("AC9D1E2F3A4B5C6D7E8F90A1B2C3D4E5", "2026-11-27T02:10:00Z", "Trung Huynh", false,
    "Update cho em biết thôi: bên anh vừa thêm voice SG-Nu-2-TM vào bộ voice dẫn đường. Không gấp đâu."),
];

test("a new message on a known item is a comment on it, not a line added to its notes",
  { skip: !claude && "set HYPOMNEMA_TEST_CLAUDE=claude to run the triage prompt with a real model", timeout: 900_000 },
  async () => {
    const { calls } = await runTriage({
      claude, model: process.env.HYPOMNEMA_TEST_CLAUDE_MODEL,
      world: { items: [item], chats: [{ service: "whatsapp", id: chat, name: "Trung Huynh", kind: "direct" }], messages },
      lines: [`- whatsapp:main chat "Trung Huynh" (id ${chat}): 1 new from Trung Huynh, latest 2026-11-27T02:10:00Z`],
    });

    const on = (name) => calls.filter((c) => c.name === name && c.args.id === item.id);
    const said = JSON.stringify(calls, null, 1);
    assert.ok(on("inbox_comment").length >= 1, `the update is a comment on the item:\n${said}`);
    assert.match(on("inbox_comment").map((c) => c.args.text).join("\n"), /SG-Nu-2-TM/, said);
    assert.deepEqual(on("inbox_update").filter((c) => "notes" in c.args), [], `the notes are left as they were:\n${said}`);
  });
