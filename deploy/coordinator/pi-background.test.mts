import assert from "node:assert/strict";
import { once } from "node:events";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { test, type TestContext } from "node:test";

import { DEFAULT_TIMEOUT_SECONDS, startBackground, stopBackground } from "./pi-background.mts";

type Request = Record<string, unknown>;

/**
 * A control plane that records the requests and answers a start with an id and a file, a stop with stopped, or every
 * one with status and a message; HOME points at a box of its own.
 */
async function controlPlane(t: TestContext, status = 200) {
  const root = mkdtempSync(join(tmpdir(), "pi-background-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const requests: Request[] = [];
  const auth: string[] = [];
  const server = createServer(async (req: IncomingMessage, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = JSON.parse(Buffer.concat(chunks).toString());
    assert.equal(req.url, "/agent/v1/background");
    requests.push(body);
    auth.push(String(req.headers.authorization));
    if (status !== 200) {
      res.writeHead(status, { "Content-Type": "application/json" }).end('{"message":"no background commands here"}');
      return;
    }
    const answer = body.kind === "start"
      ? { id: "bg_0123456789ab", output_file: "/run/bg_0123456789ab.log", timeout_seconds: body.timeout_seconds }
      : { id: body.id, stopped: true };
    res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(answer));
  });
  server.listen(0, "127.0.0.1");
  t.after(() => server.close());
  await once(server, "listening");
  const address = server.address();
  assert.ok(address && typeof address === "object");
  mkdirSync(join(root, "home", ".hypomnema"), { recursive: true });
  writeFileSync(join(root, "home", ".hypomnema", "client.json"),
    JSON.stringify({ url: `http://127.0.0.1:${address.port}/`, token: "hyp_bot_v1_pi" }));
  const before = process.env.HOME;
  process.env.HOME = join(root, "home");
  // An unset variable is deleted again: assigning undefined to process.env stores the string "undefined".
  t.after(() => {
    if (before === undefined) delete process.env.HOME;
    else process.env.HOME = before;
  });
  return { root, requests, auth };
}

test("a background command is started by the control plane, with pi's own token", async (t) => {
  const cp = await controlPlane(t);
  const started = await startBackground("make all", "/bots/maps", { commandPrefix: "set -e" });

  assert.deepEqual(cp.requests, [{ kind: "start", command: "set -e\nmake all", cwd: "/bots/maps", timeout_seconds: 1800 }]);
  assert.deepEqual(cp.auth, ["Bearer hyp_bot_v1_pi"]);
  assert.deepEqual(started, { id: "bg_0123456789ab", outputFile: "/run/bg_0123456789ab.log", timeoutSeconds: 1800 });
});

test("a background command has OpenClaw's deadline of 1,800 s unless it asks for another, in whole seconds", async (t) => {
  const cp = await controlPlane(t);
  assert.equal(DEFAULT_TIMEOUT_SECONDS, 1800);

  assert.equal((await startBackground("sleep 30", "/w", { timeout: 0.2 })).timeoutSeconds, 1);
  assert.equal((await startBackground("sleep 30", "/w", { timeout: 86400 })).timeoutSeconds, 86400);
  await assert.rejects(startBackground("sleep 30", "/w", { timeout: -1 }), /Invalid timeout/);
  assert.equal(cp.requests.length, 2, "an invalid timeout is refused before asking");
});

test("a command the control plane refuses fails with its message", async (t) => {
  await controlPlane(t, 503);

  await assert.rejects(startBackground("make", "/w"), /answered 503: no background commands here/);
});

test("background_stop asks the control plane to stop a command by its id", async (t) => {
  const cp = await controlPlane(t);

  assert.equal(await stopBackground("bg_0123456789ab"), true);
  assert.deepEqual(cp.requests, [{ kind: "stop", id: "bg_0123456789ab" }]);
});

/** Runs the pinned pi with the extension and a model that calls bash once with call; returns the model's requests. */
async function piCallingBash(t: TestContext, call: object, settings?: object): Promise<string[]> {
  const cwd = mkdtempSync(join(tmpdir(), "pi-background-pi-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const agentDir = join(cwd, "agent");
  mkdirSync(agentDir);
  if (settings) writeFileSync(join(agentDir, "settings.json"), JSON.stringify(settings));

  const requests: string[] = [];
  const model = createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk);
    requests.push(Buffer.concat(chunks).toString());
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    if (requests.length === 1) {
      for (const chunk of [
        { choices: [{ index: 0, delta: { role: "assistant", tool_calls: [{ index: 0, id: "call_1", type: "function",
          function: { name: "bash", arguments: JSON.stringify(call) } }] }, finish_reason: null }] },
        { choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }] },
      ]) res.write(`data: ${JSON.stringify({ id: "r1", object: "chat.completion.chunk", ...chunk })}\n\n`);
    }
    res.end("data: [DONE]\n\n");
  });
  model.listen(0, "127.0.0.1");
  t.after(() => model.close());
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
    "--no-context-files", "--no-prompt-templates", "--extension", resolve(import.meta.dirname, "pi-background.mts"),
    "--provider", "fixture", "--model", "stub",
  ], { cwd, env: { ...process.env, PI_CODING_AGENT_DIR: agentDir } });
  t.after(() => pi.kill());
  const lines = createInterface({ input: pi.stdout });
  t.after(() => lines.close());
  let stderr = "";
  pi.stderr.on("data", (chunk) => { stderr += String(chunk); });
  pi.stdin.write(JSON.stringify({ id: "go", type: "prompt", message: "Build it." }) + "\n");
  for await (const line of lines) {
    const reply = JSON.parse(line);
    if (reply.id === "go") assert.equal(reply.success, true, reply.error);
    if (reply.type === "agent_end") break;
  }
  assert.ok(requests.length >= 2, `pi did not answer the tool call: ${stderr}`);
  return requests;
}

function toolResult(request: string): string {
  return JSON.stringify(JSON.parse(request).messages.find((m: { role: string }) => m.role === "tool").content);
}

test("pinned pi declares bash with background and background_stop, and starts a call in the background",
  { timeout: 20_000 }, async (t) => {
    const cp = await controlPlane(t);
    const requests = await piCallingBash(t, { command: "echo from-pi", background: true });

    const tools = JSON.parse(requests[0]).tools as { function: { name: string; description: string; parameters: any } }[];
    const bash = tools.find((tool) => tool.function.name === "bash");
    assert.ok(bash, "bash is declared");
    assert.equal(bash.function.parameters.properties.background.type, "boolean");
    assert.ok(bash.function.parameters.properties.command, "pi's own parameters stay");
    assert.match(bash.function.description, /Do not sleep, poll its log/);
    assert.match(bash.function.description, /a restart of the control plane does not stop it/);
    const stop = tools.find((tool) => tool.function.name === "background_stop");
    assert.ok(stop, "background_stop is declared");
    assert.equal(stop.function.parameters.properties.id.type, "string");

    assert.equal(cp.requests.length, 1);
    assert.equal(cp.requests[0].command, "echo from-pi");
    assert.match(toolResult(requests[1]), /Started in the background as bg_0123456789ab/);
    assert.match(toolResult(requests[1]), /it is killed if it runs longer than 30m/);
    assert.match(toolResult(requests[1]), /Its output goes to \/run\/bg_0123456789ab.log/);
  });

test("bash keeps pi's shell settings, in the foreground and in the background", { timeout: 30_000 }, async (t) => {
  const settings = { shellCommandPrefix: "echo configured-prefix" };

  const foreground = await piCallingBash(t, { command: "echo body" }, settings);
  assert.match(toolResult(foreground[1]), /configured-prefix\\nbody/);

  const cp = await controlPlane(t);
  await piCallingBash(t, { command: "echo body", background: true }, settings);
  assert.equal(cp.requests[0].command, "echo configured-prefix\necho body");
});
