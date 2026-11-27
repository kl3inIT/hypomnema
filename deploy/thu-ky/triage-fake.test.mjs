// The replay cases' harness without a model (issue #1206, W6): the fake world serves the server's own tools, keeps
// the server's rules a case depends on, and every case of triage-cases.json wakes the check with the conversation
// it is about. triage.eval.mjs runs the cases with a real model; this runs in CI.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { fakeWorld, served } from "./triage-fake.mjs";
import { checkLines, worldOf } from "./triage-run.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const { cases } = JSON.parse(readFileSync(join(here, "triage-cases.json"), "utf8"));

test("the fake serves the server's tools and its Tools block", () => {
  const update = served.tools.find((t) => t.name === "inbox_update");
  assert.deepEqual(update.inputSchema.properties.why_open.enum, ["promised"]);
  for (const name of ["inbox_drop", "meeting_get", "account_read", "zalo_messages", "whatsapp_messages", "memory"]) {
    assert.ok(served.tools.some((t) => t.name === name), name);
  }
  assert.match(served.guide, /\n- Inbox: Keep the owner's to-dos/);
  assert.doesNotMatch(served.guide, /two items/);
});

test("every case wakes the check with its conversation, and names only items it has", async () => {
  for (const c of cases) {
    const world = worldOf(c);
    const { wake, lines, text } = await checkLines(c, world);
    assert.ok(wake, `${c.name}:\n${text}`);
    assert.match(lines[0], /^- /, c.name);
    const ids = new Set((c.items ?? []).map((i) => i.id));
    const e = c.expect;
    for (const id of [...(e.done ?? []).map((d) => d.id), ...(e.open ?? []), ...(e.unmoved ?? []), ...(e.dropped ?? []),
      ...(e.final ?? []).map((f) => f.id), ...(e.comments ?? []).map((m) => m.id)]) {
      assert.ok(ids.has(id), `${c.name}: ${id} is one of its items`);
    }
  }
});

// The incident of issue #1206's comment of 2026-11-27, against the fake as against the server (transitions.go).
test("the fake keeps the server's rules of who closes an ask and how it becomes a promise", () => {
  const c = cases.find((x) => x.name.startsWith("16."));
  const w = fakeWorld(worldOf(c));

  const own = w.call("inbox_done", { id: "tdo_dashboards", evidence: [{ kind: "lark_message", ref: "om_ask" }] });
  assert.match(own.error, /^forbidden: a Bot closes as its own only the to-do it is doing/);
  const handed = { ...c, items: c.items.map((i) => ({ ...i, state: "doing", assignee: { kind: "bot", id: "bot_main" } })) };
  const onAsk = fakeWorld(worldOf(handed)).call("inbox_done", { id: "tdo_dashboards",
    evidence: [{ kind: "lark_message", ref: "om_ask" }] });
  assert.match(onAsk.error, /^invalid request: a Bot closes as its own only what it did/, "the asker's message is no work");
  const pr = { kind: "pull_request", ref: "https://github.com/o/r/pull/1" };
  const unknown = fakeWorld(worldOf(handed)).call("inbox_done", { id: "tdo_dashboards", evidence: [pr] });
  assert.match(unknown.error, /^invalid request/, "a pull request of no Run of the attempt");
  const ran = { ...handed, runs: [{ id: "run_fix", pulls: [pr.ref] }] };
  const fixed = fakeWorld(worldOf(ran)).call("inbox_done", { id: "tdo_dashboards", evidence: [pr] });
  assert.equal(fixed.done_by, "bot bot_main", "work handed to the Bot closes as its own with its Run's pull request");
  const notHis = w.call("inbox_done", { id: "tdo_dashboards", for_owner: true, evidence: [{ kind: "comment", ref: "1" }] });
  assert.match(notHis.error, /^invalid request/);
  const done = w.call("inbox_done", { id: "tdo_dashboards", for_owner: true, reason: "Anh: 'gửi rồi'",
    evidence: [{ kind: "comment", ref: "2" }] });
  assert.equal(done.state, "done");
  assert.equal(done.done_by, "me");

  const sent = fakeWorld(worldOf(c)).call("inbox_done", { id: "tdo_dashboards", for_owner: true,
    evidence: [{ kind: "lark_sent", ref: "om_reply", account: "me", chat: "oc_guild" }] });
  assert.equal(sent.done_by, "me", "his reply in the thread");

  const five = cases.find((x) => x.name.startsWith("5."));
  const f = fakeWorld(worldOf(five));
  assert.match(f.call("inbox_update", { id: "tdo_reply", why_open: "waiting" }).error, /^invalid request/);
  const promised = f.call("inbox_update", { id: "tdo_reply", why_open: "promised", due_date: "none" });
  assert.equal(promised.why_open, "promised");
  assert.equal(promised.due, undefined, "due_date none clears it");
});

// Review of PR #1243: an inbox_add without a key is a new item each time, as the agent API makes one for each call;
// with the same key and request it is the first item again, and with another request a conflict (inboxstore.Create).
test("the fake makes an item for each unkeyed add, as the server does", () => {
  const w = fakeWorld({});
  const ask = { title: "Trả lời Lan", why_open: "asked_directly", quote: "anh xem giúp em" };
  const first = w.call("inbox_add", ask);
  const again = w.call("inbox_add", ask);
  assert.notEqual(first.id, again.id);
  assert.equal(w.items.length, 2);

  const keyed = w.call("inbox_add", { ...ask, key: "k1" });
  assert.equal(w.call("inbox_add", { ...ask, key: "k1" }).id, keyed.id);
  assert.match(w.call("inbox_add", { ...ask, title: "Khác", key: "k1" }).error, /^conflict/);
  assert.equal(w.items.length, 3);
});

// Review of PR #1243 (W2): waiting_on's due_date none is a due left out, due 48 hours on, as the agent API does.
test("the fake gives a move to waiting with due_date none the 48 hours to chase", () => {
  const five = cases.find((x) => x.name.startsWith("5."));
  const moved = fakeWorld(worldOf(five)).call("inbox_update", { id: "tdo_reply",
    waiting_on: { name: "Việt", quote: "em nhận", message: { kind: "zalo_message", ref: "z_took" }, due_date: "none" } });
  assert.equal(moved.due, "in 48 hours");
});

// Review round 2 of PR #1243: a keyed retry is the same request whatever the order of its fields, as the server
// compares the request it parsed (inboxstore.requestHash), not the JSON text.
test("the fake compares a keyed retry by its value, not its field order", () => {
  const w = fakeWorld({});
  const first = w.call("inbox_add", { key: "k1", title: "Trả lời Lan", quote: "anh xem giúp em",
    source: { kind: "zalo", chat: "g_team", account: "main" } });
  const again = w.call("inbox_add", { source: { account: "main", chat: "g_team", kind: "zalo" },
    quote: "anh xem giúp em", title: "Trả lời Lan", key: "k1" });
  assert.equal(again.error, undefined);
  assert.equal(again.id, first.id);
  assert.equal(w.items.length, 1);
});

// Review round 2 of PR #1243: a link is no work of the Bot's, the owner's file as any other (inboxstore botsWork).
test("the fake refuses a Bot's own close on a link", () => {
  const c = cases.find((x) => x.name.startsWith("16."));
  const handed = { ...c, items: c.items.map((i) => ({ ...i, state: "doing", assignee: { kind: "bot", id: "bot_main" } })) };
  const onLink = fakeWorld(worldOf(handed)).call("inbox_done", { id: c.items[0].id,
    evidence: [{ kind: "url", ref: "https://drive.example.com/q3-dashboards.xlsx" }] });
  assert.match(onLink.error ?? "", /^invalid request: a Bot closes as its own only what it did/);
});

// The department rule (issue #1206, owner 2026-11-27): an item a Bot adds from a chat whose department the owner
// taught (departments' chats) takes it when the Bot names none; a department named must be one of his; a chat nobody
// taught gets none. The owner's context, when a case gives it, is the run's.
test("the fake puts an item from a known chat in its department, as the server does", () => {
  const w = fakeWorld({ departments: [{ name: "Atlas", chats: [{ kind: "lark", account: "me", id: "oc_nova" }] },
    { name: "Harbor", chats: [] }] });
  assert.deepEqual(w.call("inbox_list", {}).projects, ["Atlas", "Harbor"]);
  const known = w.call("inbox_add", { title: "a", why_open: "asked_directly",
    source: { kind: "lark", chat: "oc_nova", account: "me" } });
  assert.equal(known.project, "Atlas");
  const named = w.call("inbox_add", { title: "b", why_open: "asked_directly", project: "Harbor",
    source: { kind: "lark", chat: "oc_nova", account: "me" } });
  assert.equal(named.project, "Harbor");
  const unknown = w.call("inbox_add", { title: "c", why_open: "asked_directly",
    source: { kind: "zalo", chat: "u_new", account: "main" } });
  assert.equal(unknown.project, undefined);
  assert.match(w.call("inbox_add", { title: "d", project: "Nowhere" }).error, /^not found/);
});

// Review round 4 of PR #1243: an item listed before its chat was known takes its department when updated naming none.
test("the fake puts an item updated without a department in its chat's", () => {
  const w = fakeWorld({ departments: [{ name: "Atlas", chats: [{ kind: "zalo", account: "main", id: "g_data" }] }],
    items: [{ id: "tdo_old", title: "a", state: "open", why_open: "asked_directly",
      source: { kind: "zalo", id: "g_data", account: "main" } }] });
  assert.equal(w.call("inbox_update", { id: "tdo_old", why_open: "promised" }).project, "Atlas");
});
