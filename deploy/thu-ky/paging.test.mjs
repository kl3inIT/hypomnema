// Review of #1037, round 2: the check loses no message at a page's edge, past its cap, beyond Lark's or Gmail's
// first page, or in what the prompt cannot hold.
import assert from "node:assert/strict";
import { test } from "node:test";

import { check } from "./new-messages.mjs";

const at = (seconds) => new Date(Date.parse("2026-11-21T01:00:00Z") + seconds * 1000).toISOString();
const mark = at(0);

// messagingWorld keeps messages as the servers do (messaging/zalo/store.go, whatsapp/store.go): every chat's, the
// latest first, before a time (Zalo in milliseconds, WhatsApp in seconds), a page by limit and offset.
function messagingWorld(service, messages, precision = "ms") {
  const unit = precision === "ms" ? 1 : 1000;
  const calls = [];
  const tools = [{ name: `${service}_chats`, inputSchema: { properties: { account: { enum: ["main"] } } } },
    { name: `${service}_messages` }];
  const call = async (name, args) => {
    calls.push({ name, args });
    // The chats, as kept: none known here, so each counts as a group.
    if (name === `${service}_chats`) return { chats: [] };
    if (name !== `${service}_messages` || args.chat) throw new Error(`no tool ${name}`);
    const list = messages
      .map((m, i) => ({ ...m, i }))
      .filter((m) => !args.before || Math.floor(Date.parse(m.At) / unit) < Math.floor(Date.parse(args.before) / unit))
      .sort((a, b) => Date.parse(b.At) - Date.parse(a.At) || b.i - a.i);
    const offset = args.offset ?? 0;
    return { messages: list.slice(offset, offset + (args.limit ?? 50)) };
  };
  return { tools, call, calls };
}

// counted is how many new messages the check named, by chat id.
function counted(text) {
  const out = {};
  for (const m of text.matchAll(/\(id ([^)]+)\): (\d+) new/g)) out[m[1]] = (out[m[1]] ?? 0) + Number(m[2]);
  return out;
}

const sum = (counts) => Object.values(counts).reduce((a, b) => a + b, 0);

for (const [service, precision] of [["zalo", "ms"], ["whatsapp", "s"]]) {
  test(`${service}: more messages than a page at the same time, some with the same id in two chats`, async () => {
    const messages = Array.from({ length: 501 }, (_, i) => ({
      Chat: i % 2 ? "g_b" : "g_a", ChatName: i % 2 ? "B" : "A", ID: `m${Math.floor(i / 2)}`, At: at(30),
      SenderName: "Lan",
    }));
    const w = messagingWorld(service, messages, precision);
    const got = await check({ tools: w.tools, call: w.call, state: { sources: { [`${service}:main`]: mark } },
      now: new Date(at(60)) });

    assert.equal(sum(counted(got.text)), 501, got.text);
    assert.deepEqual(got.state.sources[`${service}:main`].gaps ?? [], []);
  });
}

test("past its cap a source keeps where it stopped and reads on from there, the window never sliding", async () => {
  const messages = Array.from({ length: 250 }, (_, i) => ({
    Chat: `g_${i}`, ChatName: `G${i}`, ID: `m${i}`, At: at(2 * i + 2), SenderName: "Lan",
  }));
  const w = messagingWorld("zalo", messages);
  const config = { scanMessages: 100 };

  const first = await check({ tools: w.tools, call: w.call, config, now: new Date(at(600)) });
  const seen = counted(first.text);
  assert.equal(sum(seen), 100, "the cap counts each message");
  assert.ok(first.state.sources["zalo:main"].mark, "a first look keeps its window: it does not slide");

  const second = await check({ tools: w.tools, call: w.call, config, state: first.state, now: new Date(at(660)) });
  Object.assign(seen, counted(second.text));
  const third = await check({ tools: w.tools, call: w.call, config, state: second.state, now: new Date(at(720)) });
  Object.assign(seen, counted(third.text));

  // The first look went back an hour from at(600): every message is in it.
  assert.equal(Object.keys(seen).length, 250, "every chat is read in the end");
  const last = await check({ tools: w.tools, call: w.call, config, state: third.state, now: new Date(at(780)) });
  assert.equal(last.wake, false, last.text);
});

// larkWorld answers as Lark's API does (GET /open-apis/im/v1/chats, larksuite/cli shortcuts/im/im_chat_list.go:283-302):
// groups only unless types names p2p, pages by page_token; a chat's messages since start_time (seconds), by
// page_token too.
function larkWorld({ groups = [], p2p = [], messages = {} }) {
  const tools = [{ name: "account_read", description: "Use the account name (lark:me), API hostname" }];
  // fetched counts the messages the check was given, over every page of every chat.
  const counter = { fetched: 0 };
  const page = (items, url, size) => {
    const start = Number(url.searchParams.get("page_token") || 0);
    const next = start + size;
    return { items: items.slice(start, next), has_more: next < items.length, page_token: String(next) };
  };
  const call = async (name, args) => {
    const url = new URL(`https://open.larksuite.com${args.path}`);
    if (url.pathname === "/open-apis/authen/v1/user_info") return { status: 200, records: { code: 0, data: { open_id: "ou_me" } } };
    if (url.pathname === "/open-apis/im/v1/chats") {
      const types = (url.searchParams.get("types") ?? "group").split(",");
      const chats = [...(types.includes("p2p") ? p2p : []), ...(types.includes("group") ? groups : [])];
      return { status: 200, records: { code: 0, data: page(chats, url, Number(url.searchParams.get("page_size") ?? 20)) } };
    }
    const chat = url.searchParams.get("container_id");
    const start = Number(url.searchParams.get("start_time") ?? 0) * 1000;
    const end = Number(url.searchParams.get("end_time") ?? Infinity) * 1000;
    const list = (messages[chat] ?? []).filter((m) => Number(m.create_time) >= start && Number(m.create_time) <= end);
    if (url.searchParams.get("sort_type") === "ByCreateTimeDesc") {
      list.sort((a, b) => Number(b.create_time) - Number(a.create_time));
    }
    const data = page(list, url, Number(url.searchParams.get("page_size") ?? 20));
    counter.fetched += data.items.length;
    return { status: 200, records: { code: 0, data } };
  };
  return { tools, call, counter };
}

const larkMessage = (seconds, sender = "ou_kai") => ({
  create_time: String(Date.parse(at(seconds))), sender: { id: sender, sender_type: "user" },
});

test("lark: a direct message wakes the Bot", async () => {
  const w = larkWorld({
    p2p: [{ chat_id: "oc_kai", name: "Kai Yong", chat_mode: "p2p" }],
    messages: { oc_kai: [larkMessage(30)] },
  });
  const got = await check({ tools: w.tools, call: w.call, state: { sources: { "lark:me": mark } }, now: new Date(at(60)) });

  assert.equal(got.wake, true, got.text);
  assert.match(got.text, /lark:me chat "Kai Yong" \(id oc_kai\): 1 new/);
});

test("lark: every page of chats and of messages is read, and past the cap, the rest next run", async () => {
  const groups = Array.from({ length: 150 }, (_, i) => ({ chat_id: `oc_${i}`, name: `G${i}` }));
  const messages = Object.fromEntries(groups.map((g, i) => [g.chat_id,
    Array.from({ length: i === 120 ? 60 : 1 }, (_, j) => larkMessage(10 + j))]));
  const w = larkWorld({ groups, messages });
  const state = { sources: { "lark:me": mark } };

  const got = await check({ tools: w.tools, call: w.call, state, now: new Date(at(600)) });
  const seen = counted(got.text);
  assert.equal(Object.keys(seen).length, 150, "the second page of chats is read");
  assert.equal(seen.oc_120, 60, "and the second page of a chat's messages");
  assert.equal(got.state.sources["lark:me"].mark, at(600));

  // Review of #1037, round 3: past its cap the source keeps where it stopped, and reads on from there.
  const config = { larkChats: 50, printBytes: 1e6 };
  let kept = state;
  const read = {};
  for (let run = 0; run < 4; run++) {
    const got = await check({ tools: w.tools, call: w.call, state: kept, config, now: new Date(at(600 + run * 60)) });
    Object.assign(read, counted(got.text));
    kept = got.state;
    if (!got.wake) break;
  }
  assert.equal(Object.keys(read).length, 150, "every chat in the end, 50 a run");
  assert.deepEqual(kept.sources["lark:me"].gaps, [], "nothing is left to read");
});

function gmailWorld(count) {
  return gmailWorldOf(count).world;
}

// gmailWorldOf is gmailWorld with its mails, which a test may change between two runs.
function gmailWorldOf(count) {
  const tools = [{ name: "account_read", description: "Use the account name (google:me), API hostname" }];
  const mails = Array.from({ length: count }, (_, i) => ({
    id: `x${i}`, threadId: `t${i}`, internalDate: String(Date.parse(at(10))), labelIds: ["INBOX"],
    payload: { headers: [{ name: "From", value: `p${i}@example.com` }, { name: "Subject", value: `S${i}` }] },
  }));
  const call = async (name, args) => {
    const url = new URL(`https://gmail.googleapis.com${args.path}`);
    const id = url.pathname.match(/messages\/([^/]+)$/)?.[1];
    if (id) return { status: 200, records: mails.find((m) => m.id === id) };
    // The search's after: and before:, in seconds, as Gmail reads them.
    const q = url.searchParams.get("q") ?? "";
    const after = Number(q.match(/after:(\d+)/)?.[1] ?? 0) * 1000;
    const before = Number(q.match(/before:(\d+)/)?.[1] ?? Infinity) * 1000;
    const found = mails.filter((m) => Number(m.internalDate) > after && Number(m.internalDate) < before);
    const start = Number(url.searchParams.get("pageToken") || 0);
    const size = Number(url.searchParams.get("maxResults"));
    const next = start + size;
    return { status: 200, records: {
      messages: found.slice(start, next).map((m) => ({ id: m.id })),
      ...(next < found.length ? { nextPageToken: String(next) } : {}),
    } };
  };
  return { world: { tools, call }, mails };
}

test("gmail: every page of mail is read, and past the cap, the rest next run", async () => {
  const w = gmailWorld(150);
  const state = { sources: { "google:me": mark } };

  const got = await check({ tools: w.tools, call: w.call, state, config: { perSource: 500, printBytes: 1e6, perRun: 1e9 }, now: new Date(at(600)) });
  assert.equal(Object.keys(counted(got.text.replaceAll("thread ", "id "))).length, 150);
  assert.equal(got.state.sources["google:me"].mark, at(600));

  // Review of #1037, round 3: past its cap the source keeps where it stopped, and reads on from there.
  const config = { perSource: 100, printBytes: 1e6 };
  let kept = state;
  const read = {};
  for (let run = 0; run < 4; run++) {
    const got = await check({ tools: w.tools, call: w.call, state: kept, config, now: new Date(at(600 + run * 60)) });
    Object.assign(read, counted(got.text.replaceAll("thread ", "id ")));
    kept = got.state;
    if (!got.wake) break;
  }
  assert.equal(Object.keys(read).length, 150, "every mail in the end, 100 a run");
  assert.deepEqual(kept.sources["google:me"].gaps, [], "nothing is left to read");
});

test("whatsapp: more messages in one second than the cap are all read, a run after another", async () => {
  const messages = Array.from({ length: 7000 }, (_, i) => ({
    Chat: `c_${i}`, ChatName: `C${i}`, ID: "m1", At: at(30), SenderName: "Lan",
  }));
  const w = messagingWorld("whatsapp", messages, "s");
  let kept = { sources: { "whatsapp:main": mark } };
  const read = {};
  for (let run = 0; run < 4; run++) {
    const got = await check({ tools: w.tools, call: w.call, state: kept, config: { printBytes: 1e9, perRun: 1e9 },
      now: new Date(at(60 + run * 60)) });
    Object.assign(read, counted(got.text));
    kept = got.state;
    if (!got.wake) break;
  }
  assert.equal(Object.keys(read).length, 7000, "the 2,000 past the first run's cap are read by the next");
});

test("a conversation too long for the prompt is cut, not left to block the others", async () => {
  const messages = [
    ...Array.from({ length: 3000 }, (_, i) => ({
      Chat: "g_all", ChatName: "Everyone", ID: `a${i}`, At: at(20), SenderName: `A person with a long name ${i}`,
    })),
    { Chat: "g_pmo", ChatName: "PMO", ID: "p1", At: at(10), SenderName: "Lan" },
  ];
  const w = messagingWorld("zalo", messages);
  const got = await check({ tools: w.tools, call: w.call, state: { sources: { "zalo:main": mark } },
    now: new Date(at(60)) });

  assert.ok(Buffer.byteLength(got.text, "utf8") <= 16 << 10);
  assert.match(got.text, /\(id g_all\): 3000 new from .* and \d+ more/);
  assert.match(got.text, /\(id g_pmo\): 1 new/, "the next conversation is told too");
});

test("what the prompt cannot hold is told at the next check, never dropped", async () => {
  const messages = Array.from({ length: 400 }, (_, i) => ({
    Chat: `g_${i}`, ChatName: `A rather long group name for project number ${i} of the owner`, ID: `m${i}`,
    At: at(10 + i), SenderName: `Someone with a long name ${i}`,
  }));
  const w = messagingWorld("zalo", messages);
  let state = { sources: { "zalo:main": mark } };
  const seen = {};

  for (let run = 0; run < 5; run++) {
    const got = await check({ tools: w.tools, call: w.call, state, config: { perRun: 1e9 },
      now: new Date(at(600 + run * 60)) });
    assert.ok(Buffer.byteLength(got.text, "utf8") <= 16 << 10, `run ${run} prints ${Buffer.byteLength(got.text)} bytes`);
    Object.assign(seen, counted(got.text));
    state = got.state;
    if (!got.wake) break;
  }

  assert.equal(Object.keys(seen).length, 400, "every chat is told");
});

// Issue #1071, finding 4 of the fourth review of #1037: a capped run resumes from what it read, not from a position,
// so that what changes meanwhile neither hides a message nor counts one twice.

test("whatsapp: a late message in the second a capped run stopped in is read, and none is counted twice", async () => {
  // WhatsApp keeps seconds and lists the latest kept first within one: a message that comes late with the time of
  // the second the run stopped in comes before the ones the run counted (whatsapp/store.go: ts DESC, rowid DESC).
  const messages = Array.from({ length: 150 }, (_, i) => ({
    Chat: `c_${i}`, ChatName: `C${i}`, ID: `m${i}`, At: at(30), SenderName: "Lan",
  }));
  const w = messagingWorld("whatsapp", messages, "s");
  const config = { scanMessages: 100, printBytes: 1e9 };

  const first = await check({ tools: w.tools, call: w.call, config, state: { sources: { "whatsapp:main": mark } },
    now: new Date(at(60)) });
  const read = counted(first.text);
  assert.equal(sum(read), 100);

  messages.push({ Chat: "c_late", ChatName: "Late", ID: "late", At: at(30), SenderName: "Kai" });
  const second = await check({ tools: w.tools, call: w.call, config, state: first.state, now: new Date(at(120)) });
  const again = counted(second.text);

  assert.deepEqual(Object.keys(again).filter((chat) => read[chat]), [], "no message is counted twice");
  assert.equal(again.c_late, 1, "the late message is read");
  assert.equal(sum(read) + sum(again), 151, "every message, once");
});

test("gmail: a counted mail deleted before the capped run resumes hides no other", async () => {
  const { world: w, mails } = gmailWorldOf(150);
  const config = { perSource: 100, printBytes: 1e6 };
  const threads = (text) => counted(text.replaceAll("thread ", "id "));

  const first = await check({ tools: w.tools, call: w.call, config, state: { sources: { "google:me": mark } },
    now: new Date(at(600)) });
  const read = threads(first.text);
  assert.equal(Object.keys(read).length, 100);

  mails.splice(mails.findIndex((m) => read[m.threadId]), 1);
  const second = await check({ tools: w.tools, call: w.call, config, state: first.state, now: new Date(at(660)) });
  const again = threads(second.text);

  assert.deepEqual(Object.keys(again).filter((t) => read[t]), [], "no mail is counted twice");
  assert.equal(Object.keys(again).length, 50, "the 50 left are read");
  assert.deepEqual(second.state.sources["google:me"].gaps, [], "nothing is left to read");
});

test("lark: the cap is checked as a chat's pages are read, and the rest of the chat is read next run", async () => {
  // 500 messages of one chat, three to a second, some in the same millisecond.
  const messages = {
    oc_all: Array.from({ length: 500 }, (_, j) => ({
      ...larkMessage(10 + Math.floor(j / 3)), message_id: `om_${j}`,
    })),
  };
  const w = larkWorld({ groups: [{ chat_id: "oc_all", name: "Everyone" }], messages });
  const config = { larkMessages: 100, printBytes: 1e6 };
  let kept = { sources: { "lark:me": mark } };
  let total = 0;
  let runs = 0;

  for (; runs < 10; runs++) {
    const before = w.counter.fetched;
    const got = await check({ tools: w.tools, call: w.call, state: kept, config, now: new Date(at(600 + runs * 60)) });
    assert.ok(w.counter.fetched - before <= 150, `run ${runs} read ${w.counter.fetched - before} messages`);
    total += counted(got.text).oc_all ?? 0;
    kept = got.state;
    if (!got.wake) break;
  }

  assert.equal(total, 500, "every message, once");
  assert.ok(runs >= 5, "100 a run");
  assert.deepEqual(kept.sources["lark:me"].gaps, [], "nothing is left to read");
});
