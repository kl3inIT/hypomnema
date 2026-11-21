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
    const list = (messages[chat] ?? []).filter((m) => Number(m.create_time) >= start);
    return { status: 200, records: { code: 0, data: page(list, url, Number(url.searchParams.get("page_size") ?? 20)) } };
  };
  return { tools, call };
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

test("lark: every page of chats and of messages is read, or the source keeps its time", async () => {
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

  const capped = await check({ tools: w.tools, call: w.call, state, config: { larkChats: 50 }, now: new Date(at(600)) });
  assert.equal(capped.state.sources["lark:me"].mark, mark, "chats left unread: the source keeps its time");
  assert.match(capped.text, /could not read lark:me/);
});

function gmailWorld(count) {
  const tools = [{ name: "account_read", description: "Use the account name (google:me), API hostname" }];
  const mails = Array.from({ length: count }, (_, i) => ({
    id: `x${i}`, threadId: `t${i}`, internalDate: String(Date.parse(at(10))), labelIds: ["INBOX"],
    payload: { headers: [{ name: "From", value: `p${i}@example.com` }, { name: "Subject", value: `S${i}` }] },
  }));
  const call = async (name, args) => {
    const url = new URL(`https://gmail.googleapis.com${args.path}`);
    const id = url.pathname.match(/messages\/([^/]+)$/)?.[1];
    if (id) return { status: 200, records: mails.find((m) => m.id === id) };
    const start = Number(url.searchParams.get("pageToken") || 0);
    const size = Number(url.searchParams.get("maxResults"));
    const next = start + size;
    return { status: 200, records: {
      messages: mails.slice(start, next).map((m) => ({ id: m.id })), ...(next < count ? { nextPageToken: String(next) } : {}),
    } };
  };
  return { tools, call };
}

test("gmail: every page of mail is read, or the source keeps its time", async () => {
  const w = gmailWorld(150);
  const state = { sources: { "google:me": mark } };

  const got = await check({ tools: w.tools, call: w.call, state, config: { perSource: 500, printBytes: 1e6 }, now: new Date(at(600)) });
  assert.equal(Object.keys(counted(got.text.replaceAll("thread ", "id "))).length, 150);
  assert.equal(got.state.sources["google:me"].mark, at(600));

  const capped = await check({ tools: w.tools, call: w.call, state, config: { perSource: 100 }, now: new Date(at(600)) });
  assert.equal(capped.state.sources["google:me"].mark, mark, "mail left unread: the source keeps its time");
  assert.match(capped.text, /could not read google:me/);
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
    const got = await check({ tools: w.tools, call: w.call, state, now: new Date(at(600 + run * 60)) });
    assert.ok(Buffer.byteLength(got.text, "utf8") <= 16 << 10, `run ${run} prints ${Buffer.byteLength(got.text)} bytes`);
    Object.assign(seen, counted(got.text));
    state = got.state;
    if (!got.wake) break;
  }

  assert.equal(Object.keys(seen).length, 400, "every chat is told");
});
