// The owner's world as the triage prompt's tools answer it, for triage.test.mjs and triage.eval.mjs: an MCP server
// on stdin and stdout (newline-delimited JSON-RPC, as the MCP stdio transport is) with the Bot's Inbox tools, a Zalo
// and a WhatsApp account and the memory tool, which writes every call it gets to the file TRIAGE_WORLD_LOG names. The
// world is the file TRIAGE_WORLD names: { items, chats, messages }, a chat { service, id, name, kind } and a message
// as the server keeps it (messaging.Message), of a chat by its id.
import { appendFileSync, readFileSync } from "node:fs";
import { createInterface } from "node:readline";

const world = JSON.parse(readFileSync(process.env.TRIAGE_WORLD, "utf8"));
const log = process.env.TRIAGE_WORLD_LOG;
const items = world.items ?? [];
const chats = world.chats ?? [];
const messages = world.messages ?? [];

const text = (description) => ({ type: "string", description });
const object = (properties, required = []) => ({ type: "object", properties, required });
const link = object({ kind: text("zalo_message, whatsapp_message, lark_message, mail, meeting_line, url"), ref: text("The id") });
const due = {
  due_date: text("A calendar day, YYYY-MM-DD, in timezone."), due_at: text("A moment, RFC 3339, instead of due_date."),
  timezone: text("The owner's IANA time zone of the due, such as Asia/Ho_Chi_Minh."),
};
const waitingOn = object({ name: text("The person"), quote: text("Their words"), message: link });

const tools = [
  { name: "inbox_list", description: "List the owner's to-dos. Use it before adding one.", inputSchema: object({
    state: { type: "array", items: text("proposed, open, doing, review, done") }, source_kind: text("lark, zalo, whatsapp"),
    owner_changed_since: text("RFC 3339") }) },
  { name: "inbox_get", description: "Read one to-do with its history and comments. Use it to read an item.",
    inputSchema: object({ id: text("The to-do's id") }, ["id"]) },
  { name: "inbox_add", description: "Add a to-do. Use it for each direct ask to the owner and each promise he makes.",
    inputSchema: object({
      title: text("Title"), notes: text("Markdown"), state: text("open or proposed"), why_open: text("asked_directly, promised, waiting"),
      assignee_kind: text("me, person"), waiting_on: waitingOn,
      source: object({ kind: text("zalo, whatsapp"), chat: text("Chat id"), account: text("Label") }),
      quote: text("The words"), message: link, priority: { type: "integer" }, priority_reason: text("Why"), ...due,
    }, ["title"]) },
  { name: "inbox_update", description: "Change a to-do you added: its fields. Use it to change a field.", inputSchema: object({
    id: text("Id"), revision: { type: "integer" }, title: text("Title"), notes: text("Markdown"), done_when: text("Done when"),
    priority: { type: "integer" }, priority_reason: text("Why"), draft: text("The reply drafted"),
    hand_back: { type: "boolean" }, waiting_on: waitingOn, ...due }, ["id"]) },
  { name: "inbox_comment", description: "Post a comment in a to-do's thread, which the owner reads on the item. " +
    "Use it on an item you added or were given.", inputSchema: object({ id: text("Id"), text: text("Markdown") }, ["id", "text"]) },
  { name: "inbox_done", description: "Close a to-do as done, with evidence. Use it once the work is done or he answered.",
    inputSchema: object({ id: text("Id"), revision: { type: "integer" }, reason: text("Why"), for_owner: { type: "boolean" },
      evidence: { type: "array", items: link } }, ["id", "evidence"]) },
  ...["zalo", "whatsapp"].flatMap((service) => [
    { name: `${service}_chats`, description: `The chats kept of the owner's ${service}. Use it to find a chat.`,
      inputSchema: object({ account: { type: "string", enum: ["main"] } }) },
    { name: `${service}_messages`, description: `The messages kept of the owner's ${service}, the latest first. Use it to read a chat.`,
      inputSchema: object({ account: text("main"), chat: text("Chat id"), before: text("RFC 3339"), limit: { type: "integer" } }) },
  ]),
  { name: "memory", description: "Your memory: view, create, str_replace or insert /memories/context/owner.md. Use it to keep what you learn.",
    inputSchema: object({ command: text("view, create, str_replace, insert"), path: text("Path"), file_text: text("Text") }, ["command"]) },
];

let added = 0;

function answer(name, args) {
  const item = items.find((i) => i.id === args.id);
  const service = name.split("_")[0];
  switch (name) {
    case "inbox_list":
      return args.owner_changed_since ? { items: [], projects: [], total: 0 } : { items, projects: [], total: items.length };
    case "inbox_get":
      return item ? { ...item, events: [] } : { error: `no to-do ${args.id}` };
    case "inbox_add":
      added += 1;
      return { id: `tdo_new${added}`, revision: 1 };
    case "inbox_update":
    case "inbox_done":
      return { id: args.id, revision: (item?.revision ?? 1) + 1 };
    case "inbox_comment":
      return { id: args.id, commented: true };
    case "zalo_chats":
    case "whatsapp_chats":
      return { chats: chats.filter((c) => c.service === service).map(({ id, name: n, kind }) => ({ id, name: n, kind })) };
    case "zalo_messages":
    case "whatsapp_messages": {
      const mine = new Set(chats.filter((c) => c.service === service).map((c) => c.id));
      const before = args.before ? Date.parse(args.before) : Infinity;
      const list = messages.filter((m) => mine.has(m.Chat) && (!args.chat || m.Chat === args.chat) && Date.parse(m.At) < before)
        .sort((a, b) => Date.parse(b.At) - Date.parse(a.At));
      return { account: "main", messages: list.slice(0, args.limit ?? 50) };
    }
    case "memory":
      return args.command === "view" ? { error: "no such note" } : { ok: true };
  }
  return { error: `no tool ${name}` };
}

const send = (message) => process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);

for await (const line of createInterface({ input: process.stdin })) {
  if (!line.trim()) continue;
  const { id, method, params } = JSON.parse(line);
  if (id === undefined) continue;
  switch (method) {
    case "initialize":
      send({ id, result: { protocolVersion: params.protocolVersion, capabilities: { tools: {} },
        serverInfo: { name: "thuky", version: "1" } } });
      break;
    case "tools/list":
      send({ id, result: { tools } });
      break;
    case "tools/call": {
      appendFileSync(log, `${JSON.stringify({ name: params.name, args: params.arguments ?? {} })}\n`);
      const result = answer(params.name, params.arguments ?? {});
      send({ id, result: { content: [{ type: "text", text: JSON.stringify(result) }] } });
      break;
    }
    default:
      send({ id, error: { code: -32601, message: `no method ${method}` } });
  }
}
