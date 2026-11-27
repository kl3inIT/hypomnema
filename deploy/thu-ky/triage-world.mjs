// The owner's world as the triage prompt's tools answer it, for triage.test.mjs and triage.eval.mjs: an MCP server
// on stdin and stdout (newline-delimited JSON-RPC, as the MCP stdio transport is) with the server's own tools
// (triage-tools.json) answered by fakeWorld (triage-fake.mjs). It writes every call it gets to the file
// TRIAGE_WORLD_LOG names, and the Inbox as the calls left it to TRIAGE_WORLD_END. The world is the file TRIAGE_WORLD
// names, as triage-fake.mjs describes it.
import { appendFileSync, readFileSync, writeFileSync } from "node:fs";
import { createInterface } from "node:readline";

import { fakeWorld } from "./triage-fake.mjs";

const world = fakeWorld(JSON.parse(readFileSync(process.env.TRIAGE_WORLD, "utf8")));
const log = process.env.TRIAGE_WORLD_LOG;
const end = process.env.TRIAGE_WORLD_END;

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
      send({ id, result: { tools: world.tools } });
      break;
    case "tools/call": {
      const args = params.arguments ?? {};
      const result = world.call(params.name, args);
      appendFileSync(log, `${JSON.stringify({ name: params.name, args, id: result?.id, refused: result?.error })}\n`);
      if (end) writeFileSync(end, JSON.stringify(world.items));
      send({ id, result: { content: [{ type: "text", text: JSON.stringify(result) }], isError: Boolean(result?.error) } });
      break;
    }
    default:
      send({ id, error: { code: -32601, message: `no method ${method}` } });
  }
}
