import assert from "node:assert/strict";
import { test } from "node:test";

import { check, sources } from "./new-messages.mjs";

const now = new Date("2026-11-21T02:00:00Z");
const since = "2026-11-21T01:45:00Z";
const state = { sources: { "zalo:main": since, "google:me": since, "lark:me": since, meetings: since } };

const tools = [
  { name: "zalo_chats", inputSchema: { properties: { account: { enum: ["main"] } } } },
  { name: "zalo_messages" },
  {
    name: "account_read",
    description: "Read a connected account using its service API. Use the account name (lark:me, google:me), API hostname",
  },
  { name: "meeting_list" },
];

// world answers each tool call as the owner's accounts would; chats, messages, mail and lark hold what is there.
function world({ chats = [], messages = {}, mail = [], lark = { chats: [], messages: {} }, meetings = [] } = {}) {
  const calls = [];
  const call = async (name, args) => {
    calls.push({ name, args });
    switch (name) {
      case "zalo_chats":
        return { chats };
      case "zalo_messages":
        return { messages: messages[args.chat] ?? [] };
      case "meeting_list":
        return { meetings };
      case "account_read": {
        if (args.host === "gmail.googleapis.com") {
          const id = args.path.match(/messages\/([^?]+)\?/)?.[1];
          if (!id) return { status: 200, records: { messages: mail.map((m) => ({ id: m.id })) } };
          return { status: 200, records: mail.find((m) => m.id === id) };
        }
        if (args.path.startsWith("/open-apis/authen")) return { status: 200, records: { code: 0, data: { open_id: "ou_me" } } };
        if (args.path.startsWith("/open-apis/im/v1/chats")) {
          return { status: lark.status ?? 200, records: { code: 0, data: { items: lark.chats } } };
        }
        const chat = args.path.match(/container_id=([^&]+)/)[1];
        return { status: 200, records: { code: 0, data: { items: lark.messages[chat] ?? [] } } };
      }
    }
    throw new Error(`no tool ${name}`);
  };
  return { call, calls };
}

const mailOf = (id, from, extra = []) => ({
  id, threadId: `t_${id}`, internalDate: String(Date.parse("2026-11-21T01:50:00Z")), labelIds: ["INBOX"],
  payload: { headers: [{ name: "From", value: from }, { name: "Subject", value: `About ${id}` }, ...extra] },
});

test("the sources are the accounts the run's tools name", () => {
  assert.deepEqual(sources(tools), [
    { kind: "messaging", service: "zalo", label: "main" },
    { kind: "lark", account: "lark:me", label: "me" },
    { kind: "gmail", account: "google:me", label: "me" },
    { kind: "meetings" },
  ]);
});

test("nothing new wakes no model", async () => {
  const w = world({ chats: [{ ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: "2026-11-21T01:00:00Z" }] });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, false);
  assert.equal(got.text.split("\n").at(-1), '{"wakeAgent":false}');
  assert.equal(w.calls.filter((c) => c.name === "zalo_messages").length, 0, "a chat with nothing new is not read");
  assert.equal(got.state.sources["zalo:main"], now.toISOString());
});

test("the owner's own messages, official accounts, lists and no-reply senders are left out", async () => {
  const w = world({
    chats: [
      { ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: "2026-11-21T01:55:00Z" },
      { ID: "oa_1", Kind: "dm", Name: "Zalo Pay", LastMessageAt: "2026-11-21T01:56:00Z" },
    ],
    messages: {
      g_pmo: [
        { ID: "m1", At: "2026-11-21T01:55:00Z", FromMe: true, SenderName: "Đạt", Text: "để em check" },
        { ID: "m0", At: "2026-11-21T01:40:00Z", FromMe: false, SenderName: "Lan", Text: "older" },
      ],
      oa_1: [{ ID: "m2", At: "2026-11-21T01:56:00Z", FromMe: false, SenderName: "Zalo Pay", Text: "Ưu đãi" }],
    },
    mail: [
      mailOf("a", "GitHub <noreply@github.com>"),
      mailOf("b", "Substack <hello@substack.com>", [{ name: "List-Unsubscribe", value: "<mailto:u@x>" }]),
    ],
    lark: {
      chats: [{ chat_id: "oc_core", name: "VMAP core" }],
      messages: {
        oc_core: [
          { create_time: String(Date.parse("2026-11-21T01:58:00Z")), sender: { id: "ou_me", sender_type: "user" } },
          { create_time: String(Date.parse("2026-11-21T01:59:00Z")), sender: { id: "cli_bot", sender_type: "app" } },
        ],
      },
    },
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, false, got.text);
});

test("what is left wakes the Bot with each conversation", async () => {
  const w = world({
    chats: [{ ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: "2026-11-21T01:55:00Z" }],
    messages: {
      g_pmo: [
        { ID: "m3", At: "2026-11-21T01:55:00Z", FromMe: false, SenderName: "Lan", Text: "anh xem giúp em" },
        { ID: "m4", At: "2026-11-21T01:54:00Z", FromMe: false, SenderName: "Lan", Text: "báo giá" },
      ],
    },
    mail: [mailOf("c", "Kai Yong <kai@genaifund.sg>")],
    lark: {
      chats: [{ chat_id: "oc_core", name: "VMAP core" }],
      messages: { oc_core: [{ create_time: String(Date.parse("2026-11-21T01:58:00Z")), sender: { id: "ou_laura", sender_type: "user" } }] },
    },
    meetings: [{ id: "mtg_1", title: "VMAP standup", notes: "ready", open_actions: 2 }],
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, true);
  assert.match(got.text, /zalo:main chat "PMO" \(group, id g_pmo\): 2 new from Lan/);
  assert.match(got.text, /google:me mail thread "About c" \(thread t_c\): 1 new from Kai Yong <kai@genaifund.sg>/);
  assert.match(got.text, /lark:me chat "VMAP core" \(id oc_core\): 1 new from ou_laura/);
  assert.match(got.text, /meeting "VMAP standup" \(id mtg_1\): notes ready, 2 open actions/);
  assert.doesNotMatch(got.text, /wakeAgent/);
});

test("a source that cannot be read keeps its time and is named, without waking the Bot", async () => {
  const w = world({ lark: { status: 401, chats: [], messages: {} } });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, false);
  assert.match(got.text, /could not read lark:me: lark:me could not be read: HTTP 401/);
  assert.equal(got.state.sources["lark:me"], since, "its messages are read next time");
  assert.equal(got.state.sources["google:me"], now.toISOString());
});
