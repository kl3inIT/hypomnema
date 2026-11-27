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
  assert.match(own.error, /^invalid request: a Bot closes as its own only what it did/, "the asker's message is no work of the Bot's");
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
