import assert from "node:assert/strict";
import { once } from "node:events";
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { test } from "node:test";

import { notePath } from "./pi-read-run.mts";

test("only files in the run's notes are accepted", (test) => {
  const cwd = mkdtempSync(join(tmpdir(), "pi-notes-"));
  test.after(() => rmSync(cwd, { recursive: true, force: true }));
  mkdirSync(join(cwd, "notes"));
  writeFileSync(join(cwd, "notes", "brief.md"), "a brief");
  symlinkSync("/etc", join(cwd, "linked-dir"));
  symlinkSync("/etc/passwd", join(cwd, "linked-file"));

  assert.equal(notePath(cwd, "notes/brief.md"), join(cwd, "notes", "brief.md"));
  assert.equal(notePath(cwd, "new/brief.md"), join(cwd, "new", "brief.md"));
  for (const path of ["../secret", "/etc/passwd", ".", "notes", "linked-file", "linked-dir/passwd", "@/etc/passwd"]) {
    assert.equal(notePath(cwd, path), undefined, path);
  }
});

test("pinned pi declares the read-run tools on its first turn and refuses the RPC shell", { timeout: 20_000 }, async (test) => {
  const cwd = mkdtempSync(join(tmpdir(), "pi-read-run-"));
  test.after(() => rmSync(cwd, { recursive: true, force: true }));
  const agentDir = join(cwd, "agent");
  mkdirSync(agentDir);
  writeFileSync(join(agentDir, "mcp.json"), JSON.stringify({
    mcpServers: {
      hypomnema: {
        command: process.execPath,
        args: ["--input-type=module", "-e", `
          import { createInterface } from "node:readline";
          for await (const line of createInterface({ input: process.stdin })) {
            const { id, method, params } = JSON.parse(line);
            if (id === undefined) continue;
            const result = method === "initialize" ? {
              protocolVersion: params.protocolVersion,
              serverInfo: { name: "hypomnema", version: "test" },
              capabilities: { tools: {} },
            } : {
              tools: ["account_read", "zalo_messages"].map((name) => ({
                name, description: name, inputSchema: { type: "object" },
              })),
            };
            console.log(JSON.stringify({ jsonrpc: "2.0", id, result }));
          }
        `],
        exposure: "direct",
      },
    },
  }));

  let firstRequest: string | undefined;
  const model = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk);
    firstRequest ??= Buffer.concat(chunks).toString();
    res.writeHead(200, { "Content-Type": "text/event-stream" }).end("data: [DONE]\n\n");
  });
  model.listen(0, "127.0.0.1");
  test.after(() => model.close());
  await once(model, "listening");
  const address = model.address();
  assert.ok(address && typeof address === "object");
  writeFileSync(join(agentDir, "models.json"), JSON.stringify({
    providers: {
      fixture: {
        api: "openai-completions", baseUrl: `http://127.0.0.1:${address.port}/v1`, apiKey: "test",
        models: [{ id: "stub", name: "Stub" }],
      },
    },
  }));
  const pi = spawn(resolve(import.meta.dirname, "node_modules/.bin/pi"), [
    "--mode", "rpc", "--offline", "--no-session", "--no-approve", "--no-extensions", "--no-skills",
    "--no-context-files", "--no-prompt-templates", "--extension", "builtin:mcp",
    "--extension", resolve(import.meta.dirname, "pi-read-run.mts"),
    "--tools", "routine_read,routine_write,routine_edit,mcp__hypomnema__*", "--provider", "fixture", "--model", "stub",
  ], { cwd, env: { ...process.env, PI_CODING_AGENT_DIR: agentDir } });
  test.after(() => pi.kill());
  const lines = createInterface({ input: pi.stdout });
  test.after(() => lines.close());
  let stderr = "";
  pi.stderr.on("data", (chunk) => { stderr += String(chunk); });
  pi.stdin.write(JSON.stringify({ id: "shell", type: "bash", command: "echo shell-ran" }) + "\n");
  for await (const line of lines) {
    const reply = JSON.parse(line);
    if (reply.id === "shell") {
      assert.equal(reply.success, true);
      assert.equal(reply.data.exitCode, 1);
      assert.match(reply.data.output, /This run reads only/);
      pi.stdin.write(JSON.stringify({ id: "brief", type: "prompt", message: "Write a brief." }) + "\n");
    }
    if (reply.id === "brief") assert.equal(reply.success, true, reply.error);
    if (reply.type === "agent_end") {
      assert.ok(firstRequest, `pi did not send a model request: ${stderr}`);
      const tools = JSON.parse(firstRequest).tools.map((tool: { function: { name: string } }) => tool.function.name);
      assert.deepEqual(tools.sort(), [
        "mcp__hypomnema__account_read", "mcp__hypomnema__zalo_messages", "routine_edit", "routine_read", "routine_write",
      ]);
      return;
    }
  }
  assert.fail(`pi exited without an answer: ${stderr}`);
});
