// The owner's world as the triage prompt's tools answer it, for triage.test.mjs: an MCP server on stdin and stdout
// (newline-delimited JSON-RPC, as the MCP stdio transport is) with the Bot's Inbox tools, a WhatsApp account and
// the memory tool, which writes every call it gets to the file TRIAGE_WORLD_LOG names. The world is the file
// TRIAGE_WORLD names: { item, messages }.
import { appendFileSync, readFileSync } from "node:fs";
import { createInterface } from "node:readline";

const world = JSON.parse(readFileSync(process.env.TRIAGE_WORLD, "utf8"));
const log = process.env.TRIAGE_WORLD_LOG;

const text = (description) => ({ type: "string", description });
const object = (properties, required = []) => ({ type: "object", properties, required });
const link = object({ kind: text("zalo_message, whatsapp_message, lark_message, mail, meeting_line, url"), ref: text("The id") });

const tools = [
  { name: "inbox_list", description: "List the owner's to-dos. Use it before adding one.", inputSchema: object({
    state: { type: "array", items: text("proposed, open, doing, review, done") }, source_kind: text("lark, zalo, whatsapp"),
    owner_changed_since: text("RFC 3339") }) },
  { name: "inbox_get", description: "Read one to-do with its history and comments. Use it to read an item.",
    inputSchema: object({ id: text("The to-do's id") }, ["id"]) },
  { name: "inbox_add", description: "Add a to-do. Use it for a new thing that involves the owner.", inputSchema: object({
    title: text("Title"), notes: text("Markdown"), state: text("open or proposed"), why_open: text("asked_directly, promised, waiting"),
    assignee_kind: text("me, person"), source: object({ kind: text("whatsapp"), chat: text("Chat id"), account: text("Label") }),
    quote: text("The words"), message: link, priority: { type: "integer" }, priority_reason: text("Why") }, ["title"]) },
  { name: "inbox_update", description: "Change a to-do you added: its fields. Use it to change a field.", inputSchema: object({
    id: text("Id"), revision: { type: "integer" }, title: text("Title"), notes: text("Markdown"), done_when: text("Done when"),
    priority: { type: "integer" }, priority_reason: text("Why"), due_date: text("YYYY-MM-DD") }, ["id"]) },
  { name: "inbox_comment", description: "Post a comment in a to-do's thread, which the owner reads on the item. " +
    "Use it on an item you added or were given.", inputSchema: object({ id: text("Id"), text: text("Markdown") }, ["id", "text"]) },
  { name: "inbox_done", description: "Close a to-do as done, with evidence. Use it once the work is done.",
    inputSchema: object({ id: text("Id"), reason: text("Why"), for_owner: { type: "boolean" },
      evidence: { type: "array", items: link } }, ["id", "evidence"]) },
  { name: "whatsapp_chats", description: "The chats kept of the owner's WhatsApp. Use it to find a chat.",
    inputSchema: object({ account: { type: "string", enum: ["main"] } }) },
  { name: "whatsapp_messages", description: "The messages kept of the owner's WhatsApp, the latest first. Use it to read a chat.",
    inputSchema: object({ account: text("main"), chat: text("Chat id"), before: text("RFC 3339"), limit: { type: "integer" } }) },
  { name: "memory", description: "Your memory: view, create, str_replace or insert /memories/context/owner.md. Use it to keep what you learn.",
    inputSchema: object({ command: text("view, create, str_replace, insert"), path: text("Path"), file_text: text("Text") }, ["command"]) },
];

function answer(name, args) {
  const { item, messages } = world;
  switch (name) {
    case "inbox_list":
      return { items: args.owner_changed_since ? [] : [item], projects: [], total: args.owner_changed_since ? 0 : 1 };
    case "inbox_get":
      return args.id === item.id ? { ...item, events: [] } : { error: `no to-do ${args.id}` };
    case "inbox_add":
      return { id: "tdo_new", revision: 1 };
    case "inbox_update":
    case "inbox_done":
      return { id: args.id, revision: item.revision + 1 };
    case "inbox_comment":
      return { id: args.id, commented: true };
    case "whatsapp_chats":
      return { chats: [{ id: item.source.id, name: "Trung Huynh", kind: "direct" }] };
    case "whatsapp_messages": {
      const before = args.before ? Date.parse(args.before) : Infinity;
      const list = messages.filter((m) => (!args.chat || m.Chat === args.chat) && Date.parse(m.At) < before)
        .sort((a, b) => Date.parse(b.At) - Date.parse(a.At));
      return { messages: list.slice(0, args.limit ?? 50) };
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
