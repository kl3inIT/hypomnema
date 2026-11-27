// Issue #1101: a triage run gets a batch it can finish, direct asks first; the rest waits for the next run.
import assert from "node:assert/strict";
import { test } from "node:test";

import { check } from "./new-messages.mjs";

const at = (seconds) => new Date(Date.parse("2026-11-21T01:00:00Z") + seconds * 1000).toISOString();
const mark = at(0);

// world answers as the owner's accounts would: Zalo's kept chats and messages (messaging/zalo/store.go), Lark's chats
// and messages (open-apis/im/v1), Gmail's search, and the meetings.
function world({ chats = [], messages = [], lark = { chats: [], messages: {} }, mail = [], meetings = [] }) {
  const tools = [
    { name: "zalo_chats", inputSchema: { properties: { account: { enum: ["main"] } } } },
    { name: "zalo_messages" },
    { name: "account_read", description: "Use the account name (lark:me, google:me), API hostname. Lark accounts that sign in as the owner (lark:me); any other" },
    { name: "meeting_list" },
  ];
  const call = async (name, args) => {
    switch (name) {
      case "zalo_chats":
        return { chats: chats.slice(0, args.limit ?? 50) };
      case "zalo_messages": {
        const list = messages
          .filter((m) => !args.before || Date.parse(m.At) < Date.parse(args.before))
          .sort((a, b) => Date.parse(b.At) - Date.parse(a.At));
        return { messages: list.slice(args.offset ?? 0, (args.offset ?? 0) + (args.limit ?? 50)) };
      }
      case "meeting_list":
        return { meetings: meetings.filter((m) => Date.parse(m.ready) > Date.parse(args.since)) };
      case "account_read": {
        const url = new URL(`https://${args.host}${args.path}`);
        if (args.host === "gmail.googleapis.com") {
          const id = url.pathname.match(/messages\/([^/]+)$/)?.[1];
          if (id) return { status: 200, records: mail.find((m) => m.id === id) };
          const after = Number(url.searchParams.get("q").match(/after:(\d+)/)[1]) * 1000;
          return { status: 200, records: { messages: mail.filter((m) => Number(m.internalDate) > after) } };
        }
        if (url.pathname === "/open-apis/authen/v1/user_info") {
          return { status: 200, records: { code: 0, data: { open_id: "ou_me" } } };
        }
        if (url.pathname === "/open-apis/im/v1/chats") return { status: 200, records: { code: 0, data: { items: lark.chats } } };
        const start = Number(url.searchParams.get("start_time")) * 1000;
        const items = (lark.messages[url.searchParams.get("container_id")] ?? [])
          .filter((m) => Number(m.create_time) > start);
        return { status: 200, records: { code: 0, data: { items } } };
      }
    }
    throw new Error(`no tool ${name}`);
  };
  return { tools, call };
}

// told is the ids of the conversations a check printed, in order.
const told = (text) => [...text.matchAll(/\((?:id|thread) ([^)]+)\)/g)].map((m) => m[1]);

test("100 conversations print 15 and carry 85, and the next checks print the rest in order", async () => {
  const messages = Array.from({ length: 100 }, (_, i) => ({
    Chat: `g_${i}`, ChatName: `G${i}`, ID: `m${i}`, At: at(100 - i), SenderName: "Lan",
  }));
  const w = world({ messages });
  const first = await check({ tools: w.tools, call: w.call, state: { sources: { "zalo:main": mark } },
    now: new Date(at(600)) });

  const order = Array.from({ length: 100 }, (_, i) => `g_${i}`);
  assert.deepEqual(told(first.text), order.slice(0, 15), first.text);
  assert.equal(first.state.carry.length, 85);
  assert.match(first.text, /85 more conversations are told at the next check/);

  let state = first.state;
  const later = [];
  for (let run = 1; run <= 6; run++) {
    const got = await check({ tools: w.tools, call: w.call, state, now: new Date(at(600 + run * 60)) });
    assert.equal(got.wake, true, `check ${run} wakes the Bot for what was carried`);
    assert.ok(told(got.text).length <= 15, `check ${run} prints at most 15`);
    later.push(...told(got.text));
    state = got.state;
  }
  assert.deepEqual(later, order.slice(15), "the rest, in order, none twice");
  assert.equal(state.carry, undefined, "nothing is left to carry");
});

test("direct chats and those that tag or name the owner come first, then groups, then mail, then meetings", async () => {
  const lark = {
    chats: [
      { chat_id: "oc_team", name: "VMAP core", chat_mode: "group" },
      { chat_id: "oc_tagged", name: "PMO", chat_mode: "group" },
      { chat_id: "oc_kai", name: "Kai Yong", chat_mode: "p2p" },
    ],
    messages: {
      oc_team: [{ create_time: String(Date.parse(at(50))), sender: { id: "ou_lan", sender_type: "user" } }],
      oc_tagged: [{
        create_time: String(Date.parse(at(40))), sender: { id: "ou_lan", sender_type: "user" },
        mentions: [{ key: "@_user_1", id: "ou_me", name: "Đạt" }],
      }],
      oc_kai: [{ create_time: String(Date.parse(at(30))), sender: { id: "ou_kai", sender_type: "user" } }],
    },
  };
  const w = world({
    chats: [
      { ID: "g_named", Kind: "group", Name: "Sales" },
      { ID: "g_pmo", Kind: "group", Name: "PMO" },
      { ID: "u_lan", Kind: "dm", Name: "Lan" },
    ],
    messages: [
      { Chat: "g_named", ChatName: "Sales", ID: "z1", At: at(60), SenderName: "Minh", Text: "anh Đạt xem giúp" },
      { Chat: "g_pmo", ChatName: "PMO", ID: "z2", At: at(55), SenderName: "Minh", Text: "ok" },
      { Chat: "u_lan", ChatName: "Lan", ID: "z3", At: at(20), SenderName: "Lan", Text: "anh ơi" },
    ],
    lark,
    mail: [{
      id: "x1", threadId: "t_mail", internalDate: String(Date.parse(at(70))), labelIds: ["INBOX"],
      payload: { headers: [{ name: "From", value: "kai@genaifund.sg" }, { name: "Subject", value: "Deck" }] },
    }],
    meetings: [{ id: "mtg_1", title: "Standup", notes: "ready", open_actions: 1, ready: at(80) }],
  });
  const state = { sources: { "zalo:main": mark, "lark:me": mark, "google:me": mark, meetings: mark } };
  const config = { ownerNames: ["Đạt"] };
  const got = await check({ tools: w.tools, call: w.call, state, config, now: new Date(at(600)) });

  const order = told(got.text);
  const first = order.slice(0, 4).sort();
  assert.deepEqual(first, ["g_named", "oc_kai", "oc_tagged", "u_lan"], got.text);
  assert.deepEqual(order.slice(4, 6).sort(), ["g_pmo", "oc_team"], "then the groups");
  assert.deepEqual(order.slice(6), ["t_mail", "mtg_1"], "then mail, then meetings");

  const two = await check({ tools: w.tools, call: w.call, state, config: { ...config, perRun: 2 }, now: new Date(at(600)) });
  assert.ok(told(two.text).every((id) => ["g_named", "oc_kai", "oc_tagged", "u_lan"].includes(id)), two.text);
});

// Issue #1182: a chat with only the owner's own new messages ranks after the groups and before mail, so that the
// direct asks of others still fill the batch first (owner, 2026-11-20 and 2026-11-26).
test("a chat with only his own new messages comes after others' asks and groups, before mail", async () => {
  const w = world({
    chats: [
      { ID: "u_mine", Kind: "dm", Name: "Linh" },
      { ID: "g_pmo", Kind: "group", Name: "PMO" },
      { ID: "u_lan", Kind: "dm", Name: "Lan" },
    ],
    messages: [
      { Chat: "u_mine", ChatName: "Linh", ID: "z1", At: at(60), SenderName: "Đạt", FromMe: true, Text: "dạ oki chị" },
      { Chat: "g_pmo", ChatName: "PMO", ID: "z2", At: at(55), SenderName: "Minh", Text: "ok" },
      { Chat: "u_lan", ChatName: "Lan", ID: "z3", At: at(20), SenderName: "Lan", Text: "anh ơi" },
    ],
    mail: [{
      id: "x1", threadId: "t_mail", internalDate: String(Date.parse(at(70))), labelIds: ["INBOX"],
      payload: { headers: [{ name: "From", value: "kai@genaifund.sg" }, { name: "Subject", value: "Deck" }] },
    }],
  });
  const state = { sources: { "zalo:main": mark, "google:me": mark } };
  const got = await check({ tools: w.tools, call: w.call, state, now: new Date(at(600)) });

  assert.deepEqual(told(got.text), ["u_lan", "g_pmo", "u_mine", "t_mail"], got.text);

  const one = await check({ tools: w.tools, call: w.call, state, config: { perRun: 1 }, now: new Date(at(600)) });
  assert.deepEqual(told(one.text), ["u_lan"], "a direct ask fills the batch before his own messages");
  assert.deepEqual(one.state.carry.map((f) => f.where.match(/\(id ([^)]+)\)/)?.[1] ?? f.where), ["g_pmo", "u_mine",
    'google:me mail thread "Deck" (thread t_mail)']);
});

test("a carried chat of his own messages keeps them, and new ones from others are added", async () => {
  const w = world({ messages: [{ Chat: "u_linh", ChatName: "Linh", ID: "m2", At: at(550), SenderName: "Linh" }] });
  const carry = [{ where: 'zalo:main chat "Linh" (id u_linh)', count: 0, mine: 1, from: [], latest: at(10), rank: 2 }];
  const got = await check({ tools: w.tools, call: w.call, state: { sources: { "zalo:main": at(500) }, carry },
    now: new Date(at(600)) });

  assert.match(got.text, /\(id u_linh\): 1 new from Linh and 1 of his own/, got.text);

  const alone = await check({ tools: w.tools, call: world({}).call, state: { sources: { "zalo:main": at(500) }, carry },
    now: new Date(at(600)) });
  assert.match(alone.text, /\(id u_linh\): 1 of his own, latest/, alone.text);
});

test("with carry left and nothing new, the check still wakes the Bot", async () => {
  const w = world({});
  const carry = [{ where: 'zalo:main chat "G1" (id g_1)', count: 1, from: ["Lan"], latest: at(10), rank: 1 }];
  const got = await check({ tools: w.tools, call: w.call, state: { sources: { "zalo:main": at(500) }, carry },
    now: new Date(at(600)) });

  assert.equal(got.wake, true, got.text);
  assert.deepEqual(told(got.text), ["g_1"]);
  assert.doesNotMatch(got.text, /wakeAgent/);
});

test("a carried conversation with new messages is told once, where it was", async () => {
  const w = world({ messages: [{ Chat: "g_1", ChatName: "G1", ID: "m2", At: at(550), SenderName: "Minh" }] });
  const carry = [
    { where: 'zalo:main chat "G1" (id g_1)', count: 1, from: ["Lan"], latest: at(10), rank: 1 },
    { where: 'zalo:main chat "G2" (id g_2)', count: 1, from: ["Lan"], latest: at(11), rank: 1 },
  ];
  const got = await check({ tools: w.tools, call: w.call, state: { sources: { "zalo:main": at(500) }, carry },
    now: new Date(at(600)) });

  assert.deepEqual(told(got.text), ["g_1", "g_2"], got.text);
  assert.match(got.text, /\(id g_1\): 2 new from Lan, Minh/);
});
