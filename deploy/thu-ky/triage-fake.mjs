// fakeWorld is the owner's world as the triage's tools answer it, for triage-world.mjs (the MCP server the model
// calls) and triage.eval.mjs (the check, new-messages.mjs, that names the conversations): his Inbox, which the
// calls change, so that a case can check what it ends as; his Zalo and WhatsApp as the server keeps them, his Lark
// through account_read, his recorded meetings, and the memory tool. The tools are the server's own
// (triage-tools.json, which a Go test writes; issue #1206): their names, descriptions and schemas, so that the model
// sees what a read run of the triage sees. The Inbox's answers keep the server's rules that decide a case (who may
// close an item as done, and with what; one item per ask made a promise), with its words, as the store refuses them
// (internal/controlplane/orchestration/inboxstore/transitions.go).
//
// A world is { items, chats, messages, lark, meetings }:
// - an item as inbox_get gives it, with created_by "bot bot_main" (the Bot of the run) or "owner", and its history,
//   each event { at, revision, by, kind, after }, a comment { kind: "commented", after: { text } };
// - a chat { service, id, name, kind } and a message as the server keeps it (messaging.Message), of a chat by its id;
// - lark { me, chats, messages, threads, users }: the owner's open_id; his chats as im/v1/chats lists them; each
//   chat's messages and each thread's ({ chat, list }) as im/v1/messages gives them; a name for each open_id;
// - a meeting { id, title, notes_ready_at, summary, items, lines }, as meeting_get gives it.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

// The server's tools, as triage-tools.json holds them.
export const served = JSON.parse(readFileSync(join(here, "triage-tools.json"), "utf8"));

const bot = "bot bot_main";
const sentKinds = ["zalo_sent", "whatsapp_sent", "lark_sent"];
const messageKinds = ["zalo_message", "whatsapp_message", "lark_message"];
// canonical is a value's JSON with its keys in order: a request as the server compares it once parsed, whatever the
// order of its fields (inboxstore.requestHash).
const canonical = (v) => (v && typeof v === "object" && !Array.isArray(v)
  ? `{${Object.keys(v).sort().map((k) => `${JSON.stringify(k)}:${canonical(v[k])}`).join(",")}}`
  : Array.isArray(v) ? `[${v.map(canonical).join(",")}]` : JSON.stringify(v));
const closed = (state) => ["done", "dropped", "duplicate"].includes(state);
const refuse = (error) => ({ error });

// who is a person or Bot as inbox_get names it, "me", "person Lan", "bot bot_main", from either form.
const who = (v) => (typeof v === "object" && v ? [v.kind, v.name ?? v.id].filter(Boolean).join(" ") : v);

// itemOf is an item of a world as inbox_get gives it.
function itemOf(i) {
  const { ref, ...source } = i.source ?? {};
  return {
    revision: 1, history: [], due: i.due_date ? `${i.due_date} (Asia/Ho_Chi_Minh)` : undefined, ...i,
    assignee: who(i.assignee) ?? "me", created_by: who(i.created_by) ?? bot,
    source: ref ? { ...source, message: ref } : source,
  };
}

export function fakeWorld(world, { now = () => new Date() } = {}) {
  const items = structuredClone(world.items ?? []).map(itemOf);
  const chats = world.chats ?? [];
  const messages = world.messages ?? [];
  const lark = { me: "ou_me", chats: [], messages: {}, threads: {}, users: {}, ...(world.lark ?? {}) };
  const larkAll = () => [
    ...Object.entries(lark.messages).flatMap(([chat, list]) => list.map((m) => ({ chat_id: chat, ...m }))),
    ...Object.entries(lark.threads).flatMap(([thread, { chat, list }]) =>
      list.map((m) => ({ chat_id: chat, thread_id: thread, ...m }))),
  ];
  // sent is the message a Link of evidence names, as the server reads it: { fromMe } or undefined.
  const sent = (l) => {
    const service = l.kind.split("_")[0];
    if (service === "lark") {
      const m = larkAll().find((x) => x.message_id === l.ref && x.chat_id === l.chat);
      return m && { fromMe: m.sender?.id === lark.me };
    }
    const m = messages.find((x) => x.ID === l.ref && x.Chat === l.chat);
    return m && { fromMe: Boolean(m.FromMe) };
  };
  const meetings = world.meetings ?? [];
  // The Runs of the item's attempt, each { id, pulls }, as the control plane knows them.
  const runs = world.runs ?? [];
  // The owner's departments, each { name, chats }: the chats he put in it, each { kind, account, id } (issue #1206).
  const departments = world.departments ?? [];
  const projects = departments.map((d) => d.name);
  const chatDepartment = (src) => departments.find((d) => (d.chats ?? []).some((c) => c.kind === src.kind &&
    (c.account ?? "") === (src.account ?? "") && c.id === src.id))?.name;
  // projectOf is a project named in a call: one of his departments, none to take an item out, or a refusal.
  const projectOf = (name) => {
    if (name === undefined) return { name: undefined };
    if (String(name).toLowerCase() === "none") return { name: "" };
    const found = projects.find((p) => p.toLowerCase() === String(name).trim().toLowerCase());
    return found ? { name: found } : { error: `not found: no department ${name}; the departments: ${projects.join(", ")}` };
  };
  let added = 0;

  const find = (id) => items.find((i) => i.id === id);
  // view is an item as a tool answers it: with its history in inbox_get, and in a list of changes since a moment.
  const view = (item, history = true) => {
    const { added_with: _, key: __, history: h, ...rest } = item;
    return history ? { ...rest, history: h.slice(-20) } : rest;
  };
  const event = (item, kind, after) => {
    item.revision += 1;
    item.updated_at = now().toISOString();
    item.history.push({ at: item.updated_at, revision: item.revision, by: bot, kind, after });
  };
  const mine = (item) => item.created_by === bot || item.assignee === bot;
  const dueOf = (args) => {
    if (String(args.due_date ?? "").toLowerCase() === "none") return null;
    if (args.due_date) return `${args.due_date} (${args.timezone ?? ""})`;
    if (args.due_at) return args.due_at;
    return undefined;
  };
  const sourceOf = (args) => {
    const s = args.source ?? {};
    return {
      kind: s.kind ?? "routine", id: s.chat ?? s.thread ?? s.meeting ?? "run_triage", account: s.account,
      quote: args.quote, message: args.message,
    };
  };

  const inbox = {
    inbox_list(args) {
      const states = args.state?.length ? args.state : args.include_closed ? null : ["proposed", "open", "doing", "review"];
      const since = args.owner_changed_since ?? args.bot_changed_since;
      const by = args.owner_changed_since ? (e) => e.by === "owner" : (e) => e.by.startsWith("bot");
      let list = items.filter((i) => !states || states.includes(i.state));
      if (args.source_kind) list = list.filter((i) => i.source?.kind === args.source_kind);
      if (args.assignee_kind) list = list.filter((i) => (i.assignee ?? "me").split(" ")[0] === args.assignee_kind);
      if (since) {
        // In any state, each with those changes since then as its history.
        list = items.filter((i) => i.history.some((e) => by(e) && Date.parse(e.at) >= Date.parse(since)))
          .map((i) => ({ ...view(i, false), history: i.history.filter((e) => by(e) && Date.parse(e.at) >= Date.parse(since)) }));
        return { total: list.length, next_cursor: "", items: list, projects };
      }
      return { total: list.length, next_cursor: "", items: list.map((i) => view(i, false)), projects };
    },
    inbox_get(args) {
      const item = find(args.id);
      return item ? view(item) : refuse(`not found: no to-do ${args.id}`);
    },
    inbox_add(args) {
      // As the server: without a key each call is a new item; with a key, the same request is the first item again,
      // and another request a conflict (inboxstore.Create).
      if (args.key) {
        const { key, ...request } = args;
        const again = items.find((i) => i.key === key);
        if (again && again.added_with !== canonical(request)) {
          return refuse(`conflict: the key ${key} was used for another request`);
        }
        if (again) return view(again, false);
      }
      const project = projectOf(args.project);
      if (project.error) return refuse(project.error);
      added += 1;
      const at = now().toISOString();
      const waiting = args.waiting_on;
      const item = {
        id: `tdo_new${added}`, revision: 1, title: args.title, notes: args.notes ?? "", done_when: args.done_when,
        state: args.state ?? (args.why_open || waiting ? "open" : "proposed"), why_open: waiting ? "waiting" : args.why_open,
        assignee: waiting ? `person ${waiting.name}` : args.assignee_kind && args.assignee_kind !== "me"
          ? `${args.assignee_kind} ${args.assignee ?? ""}`.trim() : "me",
        due: dueOf(args) ?? undefined, priority: args.priority, priority_reason: args.priority_reason,
        // A department named, else the one the owner put the source chat in (inboxstore chatDepartment).
        project: project.name || chatDepartment(sourceOf(args)) || undefined,
        source: sourceOf(args), created_by: bot, created_at: at, updated_at: at, key: args.key,
        added_with: canonical((({ key: _, ...request }) => request)(args)),
        history: [{ at, revision: 1, by: bot, kind: "created", after: { title: args.title } }],
      };
      items.push(item);
      return view(item, false);
    },
    inbox_update(args) {
      const item = find(args.id);
      if (!item) return refuse(`not found: no to-do ${args.id}`);
      if (closed(item.state)) return refuse(`conflict: a ${item.state} to-do is reopened before it is edited`);
      if (!mine(item)) {
        return refuse("forbidden: a Bot edits only the to-dos it added or is given, and gives them only back to the " +
          "owner or to a Bot");
      }
      const after = {};
      if (args.waiting_on) {
        Object.assign(item, { assignee: `person ${args.waiting_on.name}`, why_open: "waiting", state: "open", draft: "" });
        item.due = dueOf(args.waiting_on) ?? "in 48 hours";
        after.waiting_on = args.waiting_on;
      }
      if (args.why_open !== undefined) {
        if (args.why_open !== "promised") {
          return refuse("invalid request: an edit makes an ask the owner's promise (why_open promised), nothing else");
        }
        if (item.created_by !== bot) return refuse("forbidden: a Bot makes a promise only of an ask it added");
        if (!["asked_directly", "promised"].includes(item.why_open) || (item.assignee ?? "me") !== "me") {
          return refuse(`invalid request: to-do ${item.id} is not an ask to the owner that is still his`);
        }
        item.why_open = after.why_open = "promised";
      }
      const due = dueOf(args);
      if (due !== undefined) item.due = after.due = due ?? undefined;
      if (args.project !== undefined) {
        const project = projectOf(args.project);
        if (project.error) return refuse(project.error);
        item.project = after.project = project.name || undefined;
      }
      for (const k of ["title", "notes", "done_when", "draft", "priority", "priority_reason"]) {
        if (args[k] !== undefined) item[k] = after[k] = args[k];
      }
      if (args.hand_back) item.assignee = after.assignee = "me";
      if (args.source) item.source = after.source = sourceOf(args);
      event(item, "edited", after);
      return view(item, false);
    },
    inbox_comment(args) {
      const item = find(args.id);
      if (!item) return refuse(`not found: no to-do ${args.id}`);
      event(item, "commented", { text: args.text });
      return view(item, false);
    },
    inbox_done(args) {
      const item = find(args.id);
      if (!item) return refuse(`not found: no to-do ${args.id}`);
      const evidence = args.evidence ?? [];
      if (closed(item.state)) return refuse(`conflict: to-do ${item.id} is ${item.state}`);
      if (args.for_owner && args.done_by) return refuse("invalid request: for_owner or done_by, not both");
      let doneBy = "me";
      if (args.for_owner) {
        const his = evidence.some((l) => sentKinds.includes(l.kind) && l.account && l.chat && sent(l)?.fromMe) ||
          evidence.some((l) => l.kind === "comment" && item.history.some((e) =>
            String(e.revision) === String(l.ref) && e.kind === "commented" && e.by === "owner"));
        if (!his) {
          return refuse("invalid request: a Bot recording for the owner gives as evidence his comment on the to-do " +
            "(comment, by its revision), his message in the to-do's chat, or the message he sent himself after it " +
            "was made in the to-do's Zalo, WhatsApp or Lark chat, or in another chat of that account where the " +
            "person who asked is a member");
        }
      } else if (args.done_by) {
        if (item.created_by === "owner") {
          return refuse(`forbidden: the owner added to-do ${item.id} himself; he records it, not a Bot on a ` +
            "person's message");
        }
        if (!String(args.reason ?? "").trim()) return refuse("invalid request: quote their message in the reason");
        if (!evidence.some((l) => messageKinds.includes(l.kind) && l.account && l.chat && sent(l)?.fromMe === false)) {
          return refuse("invalid request: a Bot recording a person's work gives as evidence their message " +
            "(zalo_message, whatsapp_message or lark_message, with the account and the chat)");
        }
        doneBy = `person ${args.done_by}`;
      } else {
        if (item.state !== "doing") return refuse("forbidden: a Bot closes as its own only the to-do it is doing");
        // Its own work on the item only, whatever its why_open (inboxstore botsWork): a Run of the world's runs (a
        // read run starts none, and sends nothing) or its pull request; never a link, a mail or anyone's message.
        const work = (l) => runs.some((r) => (l.kind === "run" && r.id === l.ref) ||
          (l.kind === "pull_request" && (r.pulls ?? []).includes(l.ref)));
        if (!evidence.every(work)) {
          return refuse("invalid request: a Bot closes as its own only what it did on this to-do in this attempt, " +
            "with that work as evidence: a Run of the attempt, its pull request, its own comment with the file it " +
            "made, or a message it sent for the item (with the account and the chat); the owner's message or " +
            "comment shows that he did it (for_owner), a person's that they did (done_by)");
        }
        doneBy = bot;
      }
      Object.assign(item, { state: "done", done_by: doneBy, close_reason: args.reason, evidence });
      event(item, "state", { state: "done", done_by: doneBy, evidence });
      return view(item, false);
    },
    inbox_drop(args) {
      const item = find(args.id);
      if (!item) return refuse(`not found: no to-do ${args.id}`);
      if (!String(args.reason ?? "").trim() || !(args.evidence ?? []).length) {
        return refuse("invalid request: a drop gives its reason and its evidence");
      }
      if (closed(item.state)) return refuse(`conflict: to-do ${item.id} is ${item.state}`);
      if (!mine(item)) return refuse(`forbidden: to-do ${item.id} is not yours`);
      Object.assign(item, { state: "dropped", close_reason: args.reason, evidence: args.evidence });
      event(item, "state", { state: "dropped", evidence: args.evidence });
      return view(item, false);
    },
    inbox_start: () => refuse("This run reads only; do not try another way."),
    inbox_release: () => refuse("This run reads only; do not try another way."),
  };

  const messaging = (service, name, args) => {
    const of = new Set(chats.filter((c) => c.service === service).map((c) => c.id));
    if (name.endsWith("_chats")) {
      return { account: "main", chats: chats.filter((c) => c.service === service).map(({ id, name: n, kind }) =>
        ({ ID: id, Name: n, Kind: kind })) };
    }
    if (!name.endsWith("_messages")) return refuse(`${name}: not kept in this world`);
    const before = args.before ? Date.parse(args.before) : Infinity;
    const list = messages.filter((m) => of.has(m.Chat) && (!args.chat || m.Chat === args.chat) &&
      Date.parse(m.At) < before).sort((a, b) => Date.parse(b.At) - Date.parse(a.At));
    const offset = args.offset ?? 0;
    return { account: "main", messages: list.slice(offset, offset + (args.limit ?? 50)) };
  };

  // larkRead answers account_read on Lark as its open API does (im-v1 chats, messages, search; authen; contact).
  const larkRead = (args) => {
    const ok = (data) => ({ status: 200, records: { code: 0, data } });
    const path = args.path ?? "";
    const query = new URLSearchParams(path.split("?")[1] ?? "");
    if (path.startsWith("/open-apis/authen/v1/user_info")) return ok({ open_id: lark.me, name: "Quân" });
    if (path.startsWith("/open-apis/contact/v3/users/")) {
      const id = path.split("?")[0].split("/").pop();
      return lark.users[id] ? ok({ user: { open_id: id, name: lark.users[id] } }) : { status: 404, records: { code: 41050 } };
    }
    if (path.startsWith("/open-apis/im/v1/chats")) return ok({ items: lark.chats, has_more: false });
    const all = larkAll();
    if (path.startsWith("/open-apis/im/v1/messages/search")) {
      const { time_range: range = {}, is_at_me: atMe, exclude_from_types: notFrom = [] } = args.body?.filter ?? {};
      const items = all
        .filter((m) => !range.start_time || Number(m.create_time) >= Date.parse(range.start_time))
        .filter((m) => !range.end_time || Number(m.create_time) <= Date.parse(range.end_time))
        .filter((m) => !atMe || (m.mentions ?? []).some((x) => x.id === lark.me))
        .filter((m) => !notFrom.includes("bot") || m.sender?.sender_type !== "app")
        .map((m) => ({ meta_data: { message_id: m.message_id, chat_id: m.chat_id, thread_id: m.thread_id } }));
      return ok({ items, has_more: false });
    }
    const one = path.match(/^\/open-apis\/im\/v1\/messages\/(om_[^/?]+)/);
    if (one) return ok({ items: all.filter((m) => m.message_id === one[1]) });
    if (path.startsWith("/open-apis/im/v1/messages")) {
      const container = query.get("container_id");
      const start = Number(query.get("start_time") ?? 0) * 1000;
      const end = query.get("end_time") ? Number(query.get("end_time")) * 1000 : Infinity;
      const list = (query.get("container_id_type") === "thread" ? lark.threads[container]?.list ?? []
        : (lark.messages[container] ?? []).filter((m) => Number(m.create_time) >= start && Number(m.create_time) <= end))
        .map((m) => ({ chat_id: lark.threads[container]?.chat ?? container, ...m }))
        .sort((a, b) => (query.get("sort_type") === "ByCreateTimeAsc" ? 1 : -1) * (Number(a.create_time) - Number(b.create_time)));
      return ok({ items: list, has_more: false });
    }
    return { status: 404, records: { code: 404, msg: "not found in this world" } };
  };

  const call = (name, args = {}) => {
    if (inbox[name]) return inbox[name](args);
    const service = name.split("_")[0];
    if (service === "zalo" || service === "whatsapp") return messaging(service, name, args);
    switch (name) {
      case "account_read":
        if (args.host === "open.larksuite.com") return larkRead(args);
        if (args.host === "gmail.googleapis.com") return { status: 200, records: { messages: [] } };
        return { status: 404, records: {} };
      case "meeting_list":
        return {
          meetings: meetings.filter((m) => !args.since || Date.parse(m.notes_ready_at) >= Date.parse(args.since))
            .map(({ id, title, notes_ready_at: at, items: actions = [] }) => ({
              id, title, recorded_at: at, updated_at: at, state: "transcribed", notes: "ready",
              actions: actions.length, open_actions: actions.filter((a) => !a.done).length,
            })),
        };
      case "meeting_get": {
        const m = meetings.find((x) => x.id === args.id);
        return m ? { ...m, next_after_line: 0 } : refuse(`not found: no meeting ${args.id}`);
      }
      case "memory":
        return args.command === "view" ? refuse("no such note") : { ok: true };
    }
    return refuse(`no tool ${name}`);
  };

  // The latest moment of the world's messages and meetings, after which the check runs.
  const latest = Math.max(0,
    ...messages.map((m) => Date.parse(m.At)),
    ...Object.values(lark.messages).flat().map((m) => Number(m.create_time)),
    ...Object.values(lark.threads).flatMap((t) => t.list).map((m) => Number(m.create_time)),
    ...meetings.map((m) => Date.parse(m.notes_ready_at)));

  return { call, items, latest, tools: served.tools };
}
