// Issue #1182: the triage prompt's cases of the owner's short replies to asks, in triage-cases.json, each run with
// triage.md and a real model through Claude Code (the gateway, ANTHROPIC_BASE_URL, on a node) against
// triage-world.mjs. It costs a model's runs, so it runs only when THU_KY_EVAL=1 is set:
//
//   THU_KY_EVAL=1 node --test deploy/thu-ky/triage.eval.mjs
//
// and its name is not *.test.mjs, so CI never runs it (inbox-zero runs its model tests only when RUN_AI_TESTS is
// set, apps/web/__tests__/ai-regression/determine-thread-status.test.ts:15-17). HYPOMNEMA_TEST_CLAUDE names the
// Claude Code to run (claude by default), HYPOMNEMA_TEST_CLAUDE_MODEL its model (its default when unset). Each case
// is a thread: the check's line is what new-messages.mjs prints for it, and what the run added, closed or moved is
// compared with what the case expects.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

import { check } from "./new-messages.mjs";
import { runTriage } from "./triage-run.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const { cases } = JSON.parse(readFileSync(join(here, "triage-cases.json"), "utf8"));
const claude = process.env.HYPOMNEMA_TEST_CLAUDE || "claude";
const model = process.env.HYPOMNEMA_TEST_CLAUDE_MODEL;

// checkLines is what new-messages.mjs prints for a case's messages after since, past its first line (the intro).
async function checkLines(c, messages) {
  const tools = [
    { name: `${c.chat.service}_chats`, inputSchema: { properties: { account: { enum: ["main"] } } } },
    { name: `${c.chat.service}_messages` },
  ];
  const call = async (name, args) => {
    if (name.endsWith("_chats")) return { chats: [{ ID: c.chat.id, Kind: c.chat.kind, Name: c.chat.name }] };
    const list = messages.filter((m) => Date.parse(m.At) < Date.parse(args.before))
      .sort((a, b) => Date.parse(b.At) - Date.parse(a.At));
    return { messages: list.slice(args.offset ?? 0, (args.offset ?? 0) + args.limit) };
  };
  const last = Math.max(...messages.map((m) => Date.parse(m.At)));
  const got = await check({ tools, call, state: { sources: { [`${c.chat.service}:main`]: c.since }, woke: c.since },
    now: new Date(last + 60_000) });
  assert.ok(got.wake, `the case's messages wake the Bot:\n${got.text}`);
  return got.text.split("\n").slice(1);
}

// canonical is a value's JSON with its keys in order, so that one call made twice compares equal.
const canonical = (v) => (v && typeof v === "object" && !Array.isArray(v)
  ? `{${Object.keys(v).sort().map((k) => `${JSON.stringify(k)}:${canonical(v[k])}`).join(",")}}`
  : Array.isArray(v) ? `[${v.map(canonical).join(",")}]` : JSON.stringify(v));

// matches says whether an inbox_add's arguments are what a matcher of the case names.
function matches(args, m) {
  const state = args.state ?? (args.why_open ? "open" : "proposed");
  if (m.why_open && args.why_open !== m.why_open) return false;
  if (m.state && state !== m.state) return false;
  if (m.due !== undefined && Boolean(args.due_date || args.due_at) !== m.due) return false;
  if (m.text && !new RegExp(m.text, "i").test(JSON.stringify(args))) return false;
  return true;
}

describe("the triage prompt's cases of his short replies (issue #1182)", {
  concurrency: 4,
  skip: process.env.THU_KY_EVAL !== "1" && "set THU_KY_EVAL=1 to run the triage prompt's cases with a real model",
}, () => {
  for (const c of cases) {
    it(c.name, { timeout: 900_000 }, async () => {
      const messages = c.messages.map((m) => ({ Chat: c.chat.id, ChatName: c.chat.name, FromMe: false, ...m }));
      const lines = await checkLines(c, messages);
      const { calls, models } = await runTriage({
        claude, model, lines, world: { items: c.items ?? [], chats: [c.chat], messages },
      });
      const said = `${lines[0]}\nmodels: ${models.join(", ")}\n${JSON.stringify(calls.filter((x) => x.name.startsWith("inbox_") &&
        x.name !== "inbox_list" && x.name !== "inbox_get"), null, 1)}`;
      // A call made again with the same arguments is one item: the model retried it (seen once in case 5).
      const adds = [...new Map(calls.filter((x) => x.name === "inbox_add").map((x) => [canonical(x.args), x.args]))
        .values()];
      const done = calls.filter((x) => x.name === "inbox_done").map((x) => x.args);
      const e = c.expect;

      const [min, max] = Array.isArray(e.adds) ? e.adds : [e.adds, e.adds];
      assert.ok(adds.length >= min && adds.length <= max, `${adds.length} items added, ${min} to ${max} expected:\n${said}`);
      for (const m of e.some ?? []) assert.ok(adds.some((a) => matches(a, m)), `an item ${JSON.stringify(m)}:\n${said}`);
      for (const m of e.none ?? []) assert.ok(!adds.some((a) => matches(a, m)), `no item ${JSON.stringify(m)}:\n${said}`);
      for (const d of e.done ?? []) {
        const closed = done.find((a) => a.id === d.id);
        assert.ok(closed, `${d.id} is closed:\n${said}`);
        if (d.evidence) assert.match(JSON.stringify(closed.evidence), new RegExp(d.evidence), `with ${d.evidence}:\n${said}`);
      }
      for (const id of e.open ?? []) assert.ok(!done.some((a) => a.id === id), `${id} stays open:\n${said}`);
      for (const id of e.unmoved ?? []) {
        assert.ok(!calls.some((x) => x.name === "inbox_update" && x.args.id === id && x.args.waiting_on),
          `${id} is not moved:\n${said}`);
      }
      if (e.unmoved) assert.ok(!adds.some((a) => a.why_open === "waiting"), `nothing waits on the teammate:\n${said}`);
      console.log(`${c.name}: passed (${models.join(", ")})\n  ${lines[0]}`);
    });
  }
});
