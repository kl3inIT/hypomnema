import assert from "node:assert/strict";
import { test } from "node:test";

import { check, ranks, sources } from "./new-messages.mjs";

const now = new Date("2026-11-21T02:00:00Z");
const since = "2026-11-21T01:45:00Z";
const state = { sources: { "zalo:main": since, "google:me": since, "lark:me": since, meetings: since } };

const tools = [
  { name: "zalo_chats", inputSchema: { properties: { account: { enum: ["main"] } } } },
  { name: "zalo_messages" },
  {
    name: "account_read",
    description: "Read a connected account using its service API. Use the account name (lark:me, google:me), API hostname, method and path with query. Lark accounts that sign in as the owner (lark:me); any other Lark account is an app's bot.",
  },
  { name: "meeting_list" },
  { name: "inbox_chats" },
];

// world answers each tool call as the owner's accounts would; chats, messages, mail and lark hold what is there.
function world({
  chats = [], messages = {}, mail = [], lark = { chats: [], messages: {} }, meetings = [], choices = [],
} = {}) {
  const calls = [];
  const call = async (name, args) => {
    calls.push({ name, args });
    switch (name) {
      case "inbox_chats":
        // The owner's choices of which chats the secretary reads (issue #1270), as the control plane answers them.
        if (choices instanceof Error) throw choices;
        return {
          defaults: {
            direct: true, group: true, bot: false,
            bot_patterns: ["^Zalo ", "\\bOA\\b", "official account", "quảng cáo", "newsletter", "@broadcast$"],
          },
          chats: choices,
        };
      case "zalo_chats":
        return { chats: chats.slice(0, args.limit ?? 50) };
      case "zalo_messages": {
        if (args.chat) return { messages: messages[args.chat] ?? [] };
        // Every chat's, the latest first, before a time, as the server keeps them (messaging/zalo/store.go).
        const all = Object.entries(messages)
          .flatMap(([chat, list]) =>
            list.map((m) => ({ Chat: chat, ChatName: chats.find((c) => c.ID === chat)?.Name ?? "", ...m })),
          )
          .filter((m) => !args.before || Date.parse(m.At) < Date.parse(args.before))
          .sort((a, b) => Date.parse(b.At) - Date.parse(a.At));
        const offset = args.offset ?? 0;
        return { messages: all.slice(offset, offset + (args.limit ?? 50)) };
      }
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
        if (args.path.startsWith("/open-apis/im/v1/messages/search")) {
          // Lark's search (im-v1/message/search): every message a chat or a thread holds in the time range, as
          // meta_data, those that tag the owner with is_at_me, an app's left out with exclude_from_types.
          if (lark.searchStatus) return { status: lark.searchStatus, records: { code: 99991679 } };
          const { time_range: range, is_at_me: atMe, exclude_from_types: notFrom = [] } = args.body.filter;
          const all = [
            ...Object.entries(lark.messages).flatMap(([chat, list]) => list.map((m) => ({ chat_id: chat, ...m }))),
            ...Object.entries(lark.threads ?? {}).flatMap(([thread, { chat, list }]) =>
              list.map((m) => ({ chat_id: chat, thread_id: thread, ...m }))),
          ];
          const items = all
            .filter((m) => Number(m.create_time) >= Date.parse(range.start_time))
            .filter((m) => Number(m.create_time) <= Date.parse(range.end_time))
            .filter((m) => !atMe || (m.mentions ?? []).some((x) => x.id === "ou_me"))
            .filter((m) => !notFrom.includes("bot") || m.sender?.sender_type !== "app")
            .map((m) => ({ meta_data: { message_id: m.message_id, chat_id: m.chat_id, thread_id: m.thread_id } }));
          return { status: 200, records: { code: 0, data: { items, has_more: false } } };
        }
        const container = args.path.match(/container_id=([^&]+)/)[1];
        if (args.path.includes("container_id_type=thread")) {
          return { status: 200, records: { code: 0, data: { items: lark.threads?.[container]?.list ?? [] } } };
        }
        return { status: 200, records: { code: 0, data: { items: lark.messages[container] ?? [] } } };
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

// Issue #1127: the Bot is given other Bots' Lark apps too; the owner's chats are read only through his own sign-in.
test("only the Lark accounts that sign in as the owner are read", () => {
  const read = (description) => sources([{ name: "account_read", description }]);
  assert.deepEqual(
    read(
      "Use the account name (lark:work, lark:me, lark:report, google:me), API hostname, method and path with query. " +
        "Lark accounts that sign in as the owner (lark:me); any other Lark account is an app's bot.",
    ),
    [
      { kind: "lark", account: "lark:me", label: "me" },
      { kind: "gmail", account: "google:me", label: "me" },
    ],
  );
  assert.deepEqual(
    read("Use the account name (lark:work, google:me), API hostname. Lark accounts that sign in as the owner (); any"),
    [{ kind: "gmail", account: "google:me", label: "me" }],
    "no Lark account signs in as the owner",
  );
  assert.deepEqual(
    read("Use the account name (lark:work, google:me), API hostname."),
    [{ kind: "gmail", account: "google:me", label: "me" }],
    "a server that does not tell which is which: no Lark account is taken for the owner's",
  );
});

test("nothing new wakes no model", async () => {
  const w = world({ chats: [{ ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: "2026-11-21T01:00:00Z" }] });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, false);
  assert.equal(got.text.split("\n").at(-1), '{"wakeAgent":false}');
  assert.equal(w.calls.filter((c) => c.name === "zalo_messages").length, 1, "one page reaches the last check");
  assert.equal(got.state.sources["zalo:main"].mark, now.toISOString());
});

test("official accounts, lists, no-reply senders and apps' messages are left out", async () => {
  const w = world({
    chats: [
      { ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: "2026-11-21T01:55:00Z" },
      { ID: "oa_1", Kind: "dm", Name: "Zalo Pay", LastMessageAt: "2026-11-21T01:56:00Z" },
    ],
    messages: {
      g_pmo: [{ ID: "m0", At: "2026-11-21T01:40:00Z", FromMe: false, SenderName: "Lan", Text: "older" }],
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
          { create_time: String(Date.parse("2026-11-21T01:59:00Z")), sender: { id: "cli_bot", sender_type: "app" } },
        ],
      },
    },
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, false, got.text);
});

// Issue #1182: on 2026-11-26 the owner answered a request for work with "dạ oki chị" and nothing read it, since the
// check counted only others' messages. His own new messages are counted apart, and wake the Bot by themselves.
test("his own short yes in a chat wakes the Bot, counted apart", async () => {
  const w = world({
    chats: [{ ID: "g_mdoc", Kind: "group", Name: "MDOC", LastMessageAt: "2026-11-21T01:55:00Z" }],
    messages: {
      g_mdoc: [
        { ID: "m1", At: "2026-11-21T01:55:00Z", FromMe: true, SenderName: "Đạt", Text: "dạ oki chị" },
        { ID: "m0", At: "2026-11-21T01:40:00Z", FromMe: false, SenderName: "Linh", Text: "em update báo cáo nhé" },
      ],
    },
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, true, got.text);
  assert.match(got.text, /zalo:main chat "MDOC" \(id g_mdoc\): 1 of his own, latest 2026-11-21T01:55:00Z/);
  assert.match(got.text, /the owner's own messages are counted apart/);
});

test("a chat with others' new messages and his own keeps their rank and says both", async () => {
  const w = world({
    chats: [{ ID: "u_lan", Kind: "dm", Name: "Lan", LastMessageAt: "2026-11-21T01:57:00Z" }],
    messages: {
      u_lan: [
        { ID: "m3", At: "2026-11-21T01:57:00Z", FromMe: true, SenderName: "Đạt", Text: "dạ chiều em gửi" },
        { ID: "m2", At: "2026-11-21T01:56:00Z", FromMe: false, SenderName: "Lan", Text: "gửi chị file nhé" },
        { ID: "m1", At: "2026-11-21T01:50:00Z", FromMe: false, SenderName: "Lan", Text: "em ơi" },
      ],
    },
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.match(got.text, /zalo:main chat "Lan" \(id u_lan\): 2 new from Lan and 1 of his own, latest 2026-11-21T01:57:00Z/);
});

test("his own Lark message wakes the Bot; an app's still does not", async () => {
  const lark = (sender) => ({
    chats: [{ chat_id: "oc_core", name: "VMAP core", chat_mode: "group" }],
    messages: { oc_core: [{ create_time: String(Date.parse("2026-11-21T01:58:00Z")), sender }] },
  });
  const mine = await check({ tools, call: world({ lark: lark({ id: "ou_me", sender_type: "user" }) }).call, state, now });
  assert.equal(mine.wake, true, mine.text);
  assert.match(mine.text, /lark:me chat "VMAP core" \(id oc_core\): 1 of his own/);

  const app = await check({ tools, call: world({ lark: lark({ id: "cli_bot", sender_type: "app" }) }).call, state, now });
  assert.equal(app.wake, false, app.text);
});

test("his sent mail in a thread wakes the Bot; a list's mail does not, and Gmail is searched with his own", async () => {
  const sent = { ...mailOf("s", "Đạt <dat@example.com>"), labelIds: ["SENT"] };
  const draft = { ...mailOf("d", "Đạt <dat@example.com>"), labelIds: ["DRAFT"] };
  const w = world({
    mail: [sent, draft, mailOf("l", "Substack <hello@substack.com>", [{ name: "List-Id", value: "<news.substack>" }])],
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.equal(got.wake, true, got.text);
  assert.match(got.text, /google:me mail thread "About s" \(thread t_s\): 1 of his own/);
  assert.doesNotMatch(got.text, /t_l|t_d/, "a list's mail and a draft are left out");
  const search = w.calls.find((c) => c.name === "account_read" && c.args.host === "gmail.googleapis.com");
  assert.doesNotMatch(decodeURIComponent(search.args.path), /-from:me/);
});

test("a message of his he took back is not counted", async () => {
  const w = world({
    chats: [{ ID: "u_lan", Kind: "dm", Name: "Lan", LastMessageAt: "2026-11-21T01:55:00Z" }],
    messages: {
      u_lan: [{ ID: "m1", At: "2026-11-21T01:55:00Z", FromMe: true, Revoked: true, SenderName: "Đạt", Text: "" }],
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
  assert.match(got.text, /zalo:main chat "PMO" \(id g_pmo\): 2 new from Lan/);
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
  assert.equal(got.state.sources["lark:me"].mark, since, "its messages are read next time");
  assert.equal(got.state.sources["google:me"].mark, now.toISOString());
});

// Review of #1037, round 1: more chats have new messages than one page holds. Every one is found, the pages going
// back to the last check, and the account's time moves on only once they all are read.
test("every chat with new messages is found, however many there are", async () => {
  const chats = [];
  const messages = {};
  for (let i = 0; i < 60; i++) {
    const id = `g_${i}`;
    chats.unshift({ ID: id, Kind: "group", Name: `Group ${i}`, LastMessageAt: new Date(Date.parse("2026-11-21T01:46:00Z") + (i * 9 + 8) * 1000).toISOString() });
    messages[id] = Array.from({ length: 9 }, (_, j) => ({
      ID: `${id}_${j}`,
      At: new Date(Date.parse("2026-11-21T01:46:00Z") + (i * 9 + j) * 1000).toISOString(),
      FromMe: false,
      SenderName: `Person ${i}`,
    }));
  }
  const w = world({ chats, messages });
  const got = await check({ tools, call: w.call, state, now, config: { perRun: 1e9 } });

  assert.equal(got.wake, true);
  for (let i = 0; i < 60; i++) assert.match(got.text, new RegExp(`chat "Group ${i}" \\(id g_${i}\\): 9 new`));
  assert.ok(w.calls.filter((c) => c.name === "zalo_messages").length >= 2, "540 messages are two pages");
  assert.equal(got.state.sources["zalo:main"].mark, now.toISOString());

  const capped = await check({ tools, call: world({ chats, messages }).call, state, now, config: { scanMessages: 100 } });
  assert.equal(capped.state.sources["zalo:main"].gaps.length, 1, "past the cap the account keeps what is left");
  assert.equal(capped.state.sources["zalo:main"].gaps[0].after, since);
  // The 101st newest of 540, one a second, is the 440th: the rest ends a second after it, none of that second counted.
  assert.equal(capped.state.sources["zalo:main"].gaps[0].before,
    new Date(Date.parse("2026-11-21T01:46:00Z") + 440 * 1000).toISOString(), "and what it read of them");
  assert.deepEqual(capped.state.sources["zalo:main"].gaps[0].seen, []);
  assert.match(capped.text, /could not read zalo:main: more than 100 new messages/);
});

// Issue #1124: a Bot given one Zalo account has no account in its tools' schemas, and the Bot copied the account's id
// from what it read instead of its label. The check names the label the reads' answers give.
test("one account is named by the label its reads give", async () => {
  const w = world({
    chats: [{ ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: "2026-11-21T01:55:00Z" }],
    messages: { g_pmo: [{ ID: "m3", At: "2026-11-21T01:55:00Z", FromMe: false, SenderName: "Lan", Text: "anh xem" }] },
  });
  const call = async (name, args) => {
    const answer = await w.call(name, args);
    return name.startsWith("zalo_") ? { account: "main", ...answer } : answer;
  };
  const one = [{ name: "zalo_chats", inputSchema: { properties: { limit: {} } } }, { name: "zalo_messages" }];
  const got = await check({ tools: one, call, state: { sources: { zalo: since } }, now });

  assert.equal(got.wake, true);
  assert.match(got.text, /zalo:main chat "PMO" \(id g_pmo\): 1 new from Lan/);
  assert.ok(got.state.sources.zalo, "its state stays under the key it had");
});

// Issue #1119, item 4: the owner corrects the Bot in his Inbox between runs (adds, drops, edits, reopens). The check
// cannot read the Inbox, so it names the moment the Bot was last woken, and the Bot lists his changes since then with
// inbox_list's owner_changed_since. A check that wakes no one keeps that moment, so that no change is missed.
test("a wake names the moment of the last one, for the owner's changes since", async () => {
  const quiet = world();
  const lastWake = "2026-11-21T01:30:00.000Z";
  const asleep = await check({ tools, call: quiet.call, state: { ...state, woke: lastWake }, now });
  assert.equal(asleep.wake, false);
  assert.equal(asleep.state.woke, lastWake, "no wake, the moment stays");

  const w = world({
    chats: [{ ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: "2026-11-21T01:55:00Z" }],
    messages: { g_pmo: [{ ID: "m3", At: "2026-11-21T01:55:00Z", FromMe: false, SenderName: "Lan", Text: "anh ơi" }] },
  });
  const got = await check({ tools, call: w.call, state: { ...state, woke: asleep.state.woke }, now });
  assert.equal(got.wake, true);
  assert.match(got.text, new RegExp(`inbox_list with owner_changed_since ${lastWake}`));
  assert.equal(got.state.woke, now.toISOString());

  const first = await check({ tools, call: w.call, state: {}, now });
  assert.match(first.text, /inbox_list with owner_changed_since 2026-11-21T01:00:00.000Z/,
    "a first wake looks back as far as the first look at the messages");
});

// Issue #1199: Lark lists only a thread's root among its chat's messages; the replies are the thread's
// (container_id_type=thread). larkAt is a time in the span from 01:45 to 02:00.
const larkAt = (time) => String(Date.parse(`2026-11-21T${time}:00Z`));
const reply = (id, time, sender, extra = {}) => ({
  message_id: id, root_id: "om_root", create_time: larkAt(time), sender: { id: sender, sender_type: "user" }, ...extra,
});

test("a reply in a Lark thread that tags the owner wakes the Bot at the direct rank", async () => {
  const w = world({
    lark: {
      chats: [
        { chat_id: "oc_core", name: "VMAP core", chat_mode: "group" },
        { chat_id: "oc_mdoc", name: "[MDOC] Community & Growth", chat_mode: "group" },
      ],
      messages: {
        oc_core: [{ message_id: "om_c", create_time: larkAt("01:50"), sender: { id: "ou_laura", sender_type: "user" } }],
        // The thread's root is older than the span: only the search finds the thread.
        oc_mdoc: [{ message_id: "om_root", thread_id: "omt_1", create_time: larkAt("01:30"), sender: { id: "ou_tuan" } }],
      },
      threads: {
        omt_1: {
          chat: "oc_mdoc",
          list: [
            reply("om_r2", "01:58", "ou_tuan", { mentions: [{ id: "ou_me", name: "DatPH" }] }),
            reply("om_r1", "01:40", "ou_tuan"),
            { message_id: "om_root", create_time: larkAt("01:30"), sender: { id: "ou_tuan", sender_type: "user" } },
          ],
        },
      },
    },
  });
  const got = await check({ tools, call: w.call, state, now, config: { perRun: 1 } });

  assert.equal(got.wake, true);
  assert.match(got.text, /lark:me chat "\[MDOC\] Community & Growth" thread omt_1 \(id oc_mdoc\): 1 new from ou_tuan/,
    "the reply is told first, its thread named, the reply before the span left out");
  assert.doesNotMatch(got.text, /could not read/);
  assert.deepEqual(got.state.carry.map((f) => [f.where, f.rank]), [['lark:me chat "VMAP core" (id oc_core)', ranks.group]]);
});

test("a tag in a Lark chat the idle stop passed is still read", async () => {
  const w = world({
    lark: {
      chats: [{ chat_id: "oc_quiet", name: "Quiet" }, { chat_id: "oc_late", name: "Late", chat_mode: "group" }],
      messages: {
        oc_late: [{
          message_id: "om_t", create_time: larkAt("01:55"), sender: { id: "ou_tuan", sender_type: "user" },
          mentions: [{ id: "ou_me" }],
        }],
      },
    },
  });
  const got = await check({ tools, call: w.call, state, now, config: { larkIdleStop: 1, perRun: 1 } });

  assert.match(got.text, /lark:me chat "Late" \(id oc_late\): 1 new from ou_tuan/);
  assert.equal(got.state.carry, undefined, "nothing else");
});

test("a reply in a Lark thread without a tag counts as the group's, in a thread of the span or an older one", async () => {
  const w = world({
    lark: {
      chats: [{ chat_id: "oc_core", name: "VMAP core", chat_mode: "group" }],
      messages: {
        oc_core: [
          { message_id: "om_root", thread_id: "omt_new", create_time: larkAt("01:50"), sender: { id: "ou_laura", sender_type: "user" } },
          { message_id: "om_old", thread_id: "omt_old", create_time: larkAt("01:00"), sender: { id: "ou_lan", sender_type: "user" } },
        ],
      },
      threads: {
        omt_new: { chat: "oc_core", list: [reply("om_n1", "01:52", "ou_kai")] },
        omt_old: { chat: "oc_core", list: [reply("om_o1", "01:55", "ou_lan", { root_id: "om_old" })] },
      },
    },
  });
  const got = await check({ tools, call: w.call, state, now, config: { perRun: 0 } });

  assert.deepEqual(got.state.carry.map((f) => [f.where, f.count, f.from, f.rank]), [
    ['lark:me chat "VMAP core" (id oc_core)', 1, ["ou_laura"], ranks.group],
    ['lark:me chat "VMAP core" thread omt_new (id oc_core)', 1, ["ou_kai"], ranks.group],
    ['lark:me chat "VMAP core" thread omt_old (id oc_core)', 1, ["ou_lan"], ranks.group],
  ]);
});

// Issue #1182: the owner's own reply in a thread is counted apart, ranked after the others' conversations; an
// app's is left out.
test("the owner's own reply in a Lark thread is counted apart, and an app's left out", async () => {
  const w = world({
    lark: {
      chats: [{ chat_id: "oc_core", name: "VMAP core", chat_mode: "group" }],
      messages: {},
      threads: {
        omt_1: {
          chat: "oc_core",
          list: [
            reply("om_2", "01:58", "ou_me"),
            { ...reply("om_1", "01:56", "cli_bot"), sender: { id: "cli_bot", sender_type: "app" } },
          ],
        },
      },
    },
  });
  const got = await check({ tools, call: w.call, state, now, config: { perRun: 0 } });

  assert.deepEqual(got.state.carry.map((f) => [f.where, f.count, f.mine, f.rank]), [
    ['lark:me chat "VMAP core" thread omt_1 (id oc_core)', 0, 1, ranks.mine],
  ]);
});

test("a Lark search refused is named, and the threads of the span's roots are still read", async () => {
  const w = world({
    lark: {
      searchStatus: 403,
      chats: [{ chat_id: "oc_core", name: "VMAP core", chat_mode: "group" }],
      messages: {
        oc_core: [{ message_id: "om_root", thread_id: "omt_1", create_time: larkAt("01:50"), sender: { id: "ou_me" } }],
      },
      threads: { omt_1: { chat: "oc_core", list: [reply("om_1", "01:52", "ou_kai")] } },
    },
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.match(got.text, /lark:me chat "VMAP core" thread omt_1 \(id oc_core\): 1 new from ou_kai/,
    "a reply to the owner's own thread is counted");
  assert.match(got.text, /could not read lark:me: the search of the messages that tag the owner failed \(lark:me could not be read: HTTP 403\)/);
  assert.equal(got.state.sources["lark:me"].mark, now.toISOString(), "the source moves on");
});

test("a Lark thread found before the cap is read next run, its chat not counted again", async () => {
  const lark = {
    chats: [{ chat_id: "oc_core", name: "VMAP core", chat_mode: "group" }],
    messages: {
      oc_core: [{ message_id: "om_root", thread_id: "omt_1", create_time: larkAt("01:50"), sender: { id: "ou_laura", sender_type: "user" } }],
    },
    threads: { omt_1: { chat: "oc_core", list: [reply("om_1", "01:52", "ou_kai")] } },
  };
  const first = await check({ tools, call: world({ lark }).call, state, now, config: { larkMessages: 1 } });
  assert.match(first.text, /chat "VMAP core" \(id oc_core\): 1 new from ou_laura/);
  assert.doesNotMatch(first.text, /thread omt_1/);
  assert.deepEqual(first.state.sources["lark:me"].gaps[0].threads, [["omt_1", "oc_core"]], "the thread is kept to read");

  const later = new Date(now.getTime() + 60_000);
  const next = await check({ tools, call: world({ lark }).call, state: { sources: first.state.sources }, now: later });
  assert.match(next.text, /chat "VMAP core" thread omt_1 \(id oc_core\): 1 new from ou_kai/);
  assert.doesNotMatch(next.text, /\(id oc_core\): 1 new from ou_laura/, "the chat was read through");
  assert.deepEqual(next.state.sources["lark:me"].gaps, []);
});

// Issue #1270: the owner switches chats off and on for the secretary on his Inbox's chats screen, kept by the control
// plane per account and chat. The check reads his choices (inbox_chats) and skips a chat he switched off before any
// model, reads a bot's chat he switched on, and no longer reads chat ids from dropChats of filters.json. Every name
// and id here is made up.

test("a chat the owner switched off is skipped, one he switched on is read", async () => {
  const at = "2026-11-21T01:55:00Z";
  const w = world({
    chats: [
      { ID: "u_lan", Kind: "dm", Name: "Lan", LastMessageAt: at },
      { ID: "oa_pay", Kind: "dm", Name: "Zalo Pay", LastMessageAt: at },
      { ID: "oa_shop", Kind: "dm", Name: "Shop ABC OA", LastMessageAt: at },
      { ID: "g_pmo", Kind: "group", Name: "PMO", LastMessageAt: at },
    ],
    messages: {
      u_lan: [{ ID: "m1", At: at, FromMe: false, SenderName: "Lan", Text: "anh ơi" }],
      oa_pay: [{ ID: "m2", At: at, FromMe: false, SenderName: "Zalo Pay", Text: "Hóa đơn tháng 11" }],
      oa_shop: [{ ID: "m3", At: at, FromMe: false, SenderName: "Shop ABC OA", Text: "Sale" }],
      g_pmo: [{ ID: "m4", At: at, FromMe: false, SenderName: "Minh", Text: "họp lúc 3h" }],
    },
    lark: {
      chats: [
        { chat_id: "oc_off", name: "Family", chat_mode: "group" },
        { chat_id: "oc_core", name: "VMAP core", chat_mode: "group" },
      ],
      messages: {
        oc_off: [{ create_time: String(Date.parse(at)), sender: { id: "ou_mai", sender_type: "user" } }],
        oc_core: [{ create_time: String(Date.parse(at)), sender: { id: "ou_hung", sender_type: "user" } }],
      },
    },
    choices: [
      { account: "zalo:main", chat: "u_lan", reads: false, set_at: since },
      { account: "zalo:main", chat: "oa_pay", reads: true, set_at: since },
      { account: "lark:me", chat: "oc_off", reads: false, set_at: since },
      { account: "zalo:other", chat: "g_pmo", reads: false, set_at: since },
    ],
  });
  // Today's filters.json: the defaults and one chat by its exact id, which the one-time move made his choice.
  const config = { dropChats: ["^Zalo ", "\\bOA\\b", "^g_pmo$"] };
  const got = await check({ tools, call: w.call, state, now, config });

  assert.equal(got.wake, true, got.text);
  assert.doesNotMatch(got.text, /u_lan/, "switched off by the owner");
  assert.doesNotMatch(got.text, /oc_off/, "switched off by the owner");
  assert.doesNotMatch(got.text, /oa_shop/, "a bot's by default");
  assert.match(got.text, /zalo:main chat "Zalo Pay" \(id oa_pay\): 1 new/, "a bot's switched on");
  assert.match(got.text, /zalo:main chat "PMO" \(id g_pmo\)/, "dropChats ids are his choices now; another account's choice is not this one's");
  assert.match(got.text, /lark:me chat "VMAP core"/);
  assert.match(got.text, /chats the owner switched off/);
  assert.equal(w.calls.filter((c) => c.name === "inbox_chats").length, 1, "read once a run");
  assert.ok(!w.calls.some((c) => c.args?.path?.includes("container_id=oc_off")), "nothing of it is read");
});

test("when his choices cannot be read, his chats wait for the next run", async () => {
  const at = "2026-11-21T01:55:00Z";
  const w = world({
    chats: [{ ID: "u_lan", Kind: "dm", Name: "Lan", LastMessageAt: at }],
    messages: { u_lan: [{ ID: "m1", At: at, FromMe: false, SenderName: "Lan", Text: "anh ơi" }] },
    mail: [mailOf("a", "Kai <kai@example.com>")],
    choices: new Error("the control plane is restarting"),
  });
  const got = await check({ tools, call: w.call, state, now });

  assert.doesNotMatch(got.text, /u_lan/);
  assert.match(got.text, /could not read zalo:main: which chats the owner switched off could not be read/);
  assert.match(got.text, /could not read lark:me: which chats the owner switched off could not be read/);
  assert.match(got.text, /google:me mail thread "About a"/, "mail is no chat: it is still read");
  assert.equal(got.state.sources["zalo:main"], since, "its time is kept");
  assert.ok(!w.calls.some((c) => c.name === "zalo_messages"));
});

// Review of #1270, round 1: what a run had no room for is carried to the next one, and the owner may switch a carried
// chat off meanwhile. The carried conversations get his choices of the run that prints them: a chat he switched off
// is dropped before anything is printed; when his choices cannot be read, a carried chat is kept, not printed, and
// wakes nobody, while a carried mail thread is still told.
test("a carried chat he switched off since is never printed", async () => {
  const at = "2026-11-21T01:55:00Z";
  const chats = Array.from({ length: 16 }, (_, i) => ({ ID: `g_${i}`, Kind: "group", Name: `G${i}`, LastMessageAt: at }));
  const messages = Object.fromEntries(chats.map((c, i) => [c.ID, [{ ID: `m${i}`, At: at, FromMe: false, SenderName: "Lan", Text: "?" }]]));
  const first = await check({ tools, call: world({ chats, messages }).call, state, now });
  assert.equal(first.state.carry.length, 1, first.text);
  const [left] = first.state.carry;
  const id = left.where.match(/\(id ([^)]+)\)/)[1];

  const off = world({ choices: [{ account: "zalo:main", chat: id, reads: false, set_at: since }] });
  const next = await check({ tools, call: off.call, state: first.state, now: new Date(now.getTime() + 15 * 60_000) });

  assert.equal(next.wake, false, next.text);
  assert.doesNotMatch(next.text, new RegExp(`id ${id}\\b`));
  assert.equal(next.state.carry, undefined, "dropped from the queue");
});

test("a carried chat whose choice cannot be read is kept, not printed, and wakes nobody", async () => {
  const carry = [
    { where: 'zalo:main chat "Lan" (id u_lan)', count: 1, from: ["Lan"], latest: since, rank: ranks.direct },
    { where: 'lark:me chat "VMAP core" thread omt_1 (id oc_core)', count: 1, from: ["ou_hung"], latest: since, rank: ranks.group },
  ];
  const w = world({ choices: new Error("the control plane is restarting") });
  const got = await check({ tools, call: w.call, state: { ...state, carry }, now });

  assert.equal(got.wake, false, got.text);
  assert.doesNotMatch(got.text, /u_lan|oc_core/);
  assert.deepEqual(got.state.carry, carry, "kept for the run that can check them");

  const mail = { where: 'google:me mail thread "Deck" (thread t_deck)', count: 1, from: ["Kai"], latest: since, rank: ranks.mail };
  const withMail = await check({ tools, call: w.call, state: { ...state, carry: [...carry, mail] }, now });
  assert.equal(withMail.wake, true, withMail.text);
  assert.match(withMail.text, /mail thread "Deck"/);
  assert.doesNotMatch(withMail.text, /u_lan|oc_core/);
  assert.deepEqual(withMail.state.carry, carry);
});

// Review of #1270, round 1: a run without inbox_chats (the Bot's Inbox tools switched off) cannot know which chats
// the owner switched off: it reads none of his chats, as when the tool fails, rather than the defaults.
test("without inbox_chats his chats are not read", async () => {
  const at = "2026-11-21T01:55:00Z";
  const w = world({
    chats: [{ ID: "u_lan", Kind: "dm", Name: "Lan", LastMessageAt: at }],
    messages: { u_lan: [{ ID: "m1", At: at, FromMe: false, SenderName: "Lan", Text: "anh ơi" }] },
  });
  const got = await check({ tools: tools.filter((t) => t.name !== "inbox_chats"), call: w.call, state, now });

  assert.doesNotMatch(got.text, /u_lan/);
  assert.match(got.text, /could not read zalo:main: which chats the owner switched off could not be read \(this run has no inbox_chats/);
  assert.ok(!w.calls.some((c) => c.name === "zalo_messages"));
});
