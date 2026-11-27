// Issue #1182, rebuilt on one model in #1206: the triage prompt's replay cases, in triage-cases.json, each run with
// triage.md and a real model through Claude Code (the gateway, ANTHROPIC_BASE_URL, on a node) against
// triage-world.mjs, the server's own tools (triage-tools.json) over a fake of the owner's world (triage-fake.mjs). It
// costs a model's runs, so it runs only when THU_KY_EVAL=1 is set:
//
//   THU_KY_EVAL=1 [THU_KY_CASES=<file>] [THU_KY_PROMPT=<file>] node --test deploy/thu-ky/triage.eval.mjs
//
// and its name is not *.test.mjs, so CI never runs it (inbox-zero runs its model tests only when RUN_AI_TESTS is
// set, apps/web/__tests__/ai-regression/determine-thread-status.test.ts:15-17). THU_KY_CASES names another file of
// cases, such as the real ones kept in the main Bot's folder on zm (thu-ky/triage-cases.real.json; owner,
// 2026-11-27: real cases stay private, the repository is public); THU_KY_PROMPT another prompt, such as main's
// triage.md for a baseline. HYPOMNEMA_TEST_CLAUDE names the Claude Code to run (claude by default),
// HYPOMNEMA_TEST_CLAUDE_MODEL its model (its default when unset). Each case is a world: the check's lines are what
// new-messages.mjs prints for it, and what the run added, changed and closed, and the Inbox it left, are compared
// with what the case expects (README.md, "The replay cases").
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

import { checkLines, runTriage, worldOf } from "./triage-run.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const casesFile = process.env.THU_KY_CASES ? resolve(process.env.THU_KY_CASES) : join(here, "triage-cases.json");
const prompt = process.env.THU_KY_PROMPT ? resolve(process.env.THU_KY_PROMPT) : join(here, "triage.md");
const { cases } = JSON.parse(readFileSync(casesFile, "utf8"));
const claude = process.env.HYPOMNEMA_TEST_CLAUDE || "claude";
const model = process.env.HYPOMNEMA_TEST_CLAUDE_MODEL;

// matches says whether an inbox_add's arguments are what a matcher of the case names.
function matches(args, m) {
  const state = args.state ?? (args.why_open || args.waiting_on ? "open" : "proposed");
  if (m.why_open && (args.waiting_on ? "waiting" : args.why_open) !== m.why_open) return false;
  if (m.state && state !== m.state) return false;
  if (m.due !== undefined && Boolean(args.due_date && args.due_date !== "none" || args.due_at) !== m.due) return false;
  if (m.text && !new RegExp(m.text, "i").test(JSON.stringify(args))) return false;
  return true;
}

// holds says whether an item of the Inbox the run left is what a matcher names.
function holds(item, m) {
  if (m.why_open && item.why_open !== m.why_open) return false;
  if (m.state && item.state !== m.state) return false;
  if (m.due !== undefined && Boolean(item.due) !== m.due) return false;
  if (m.due_text && !new RegExp(m.due_text).test(item.due ?? "")) return false;
  if (m.project !== undefined && (item.project ?? "") !== m.project) return false;
  if (m.text && !new RegExp(m.text, "i").test(JSON.stringify(item))) return false;
  return true;
}

const closed = (item) => ["done", "dropped", "duplicate"].includes(item?.state);

describe("the triage prompt's replay cases (issues #1182, #1206)", {
  concurrency: 4,
  skip: process.env.THU_KY_EVAL !== "1" && "set THU_KY_EVAL=1 to run the triage prompt's cases with a real model",
}, () => {
  for (const c of cases) {
    it(c.name, { timeout: 900_000 }, async () => {
      const world = worldOf(c);
      const { text, wake, lines } = await checkLines(c, world);
      assert.ok(wake, `the case's messages wake the Bot:\n${text}`);
      const { calls, models, items } = await runTriage({ claude, model, lines, world, prompt });
      const byId = new Map(items.map((i) => [i.id, i]));
      const said = `${lines.join("\n")}\nmodels: ${models.join(", ")}\n${JSON.stringify(calls.filter((x) =>
        x.name.startsWith("inbox_") && x.name !== "inbox_list" && x.name !== "inbox_get"), null, 1)}`;
      // The items the run made, as the server makes them (review of PR #1243): an add without a key is a new item
      // each time, so a retry counts twice; one with a key the server answered with its first item counts once.
      const adds = [...new Map(calls.filter((x) => x.name === "inbox_add" && !x.refused && x.id)
        .map((x) => [x.id, x.args])).values()];
      const e = c.expect;

      const [min, max] = Array.isArray(e.adds) ? e.adds : [e.adds ?? 0, e.adds ?? 0];
      assert.ok(adds.length >= min && adds.length <= max, `${adds.length} items added, ${min} to ${max} expected:\n${said}`);
      for (const m of e.some ?? []) assert.ok(adds.some((a) => matches(a, m)), `an item ${JSON.stringify(m)}:\n${said}`);
      for (const m of e.none ?? []) assert.ok(!adds.some((a) => matches(a, m)), `no item ${JSON.stringify(m)}:\n${said}`);
      for (const d of e.done ?? []) {
        const item = byId.get(d.id);
        assert.equal(item?.state, "done", `${d.id} is closed:\n${said}`);
        if (d.evidence) assert.match(JSON.stringify(item.evidence), new RegExp(d.evidence), `with ${d.evidence}:\n${said}`);
        if (d.done_by) assert.match(item.done_by, new RegExp(`^${d.done_by}`), `done by ${d.done_by}:\n${said}`);
      }
      for (const id of e.dropped ?? []) assert.equal(byId.get(id)?.state, "dropped", `${id} is dropped:\n${said}`);
      for (const id of e.open ?? []) assert.ok(!closed(byId.get(id)), `${id} stays open:\n${said}`);
      for (const f of e.final ?? []) {
        assert.ok(holds(byId.get(f.id) ?? {}, f), `${f.id} ends ${JSON.stringify(f)}:\n${JSON.stringify(byId.get(f.id))}\n${said}`);
      }
      for (const n of e.count ?? []) {
        const got = items.filter((i) => !closed(i) && holds(i, n)).length;
        assert.equal(got, n.n, `${n.n} open items ${JSON.stringify(n)}, ${got} found:\n${said}`);
      }
      for (const m of e.comments ?? []) {
        const on = (byId.get(m.id)?.history ?? []).filter((h) => h.kind === "commented" && h.by.startsWith("bot"));
        assert.ok(on.some((h) => !m.text || new RegExp(m.text, "i").test(h.after.text)), `a comment on ${m.id}:\n${said}`);
      }
      for (const id of e.unmoved ?? []) {
        assert.ok(!calls.some((x) => x.name === "inbox_update" && x.args.id === id && x.args.waiting_on && !x.refused),
          `${id} is not moved:\n${said}`);
      }
      if (e.unmoved) assert.ok(!adds.some((a) => a.why_open === "waiting" || a.waiting_on), `nothing waits on the teammate:\n${said}`);
      console.log(`${c.name}: passed (${models.join(", ")})\n  ${lines[0]}`);
    });
  }
});
