import assert from "node:assert/strict";
import { once } from "node:events";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { test, type TestContext } from "node:test";

import { DEFAULT_TIMEOUT_SECONDS, Output, startBackground, TAIL_CHARS } from "./pi-background.mts";

type Report = Record<string, unknown>;

/** A control plane that records the reports and answers each with status; HOME and TMPDIR point at a box of its own. */
async function controlPlane(t: TestContext, status = 200) {
  const root = mkdtempSync(join(tmpdir(), "pi-background-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const reports: Report[] = [];
  const auth: string[] = [];
  let ended: (r: Report) => void = () => {};
  const end = new Promise<Report>((r) => (ended = r));
  const server = createServer(async (req: IncomingMessage, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = JSON.parse(Buffer.concat(chunks).toString());
    assert.equal(req.url, "/agent/v1/background");
    reports.push(body);
    auth.push(String(req.headers.authorization));
    res.writeHead(status, { "Content-Type": "application/json" }).end(status === 200 ? '{"ok":true}' : '{"message":"no"}');
    if (body.kind === "ended") ended(body);
  });
  server.listen(0, "127.0.0.1");
  t.after(() => server.close());
  await once(server, "listening");
  const address = server.address();
  assert.ok(address && typeof address === "object");
  mkdirSync(join(root, "home", ".hypomnema"), { recursive: true });
  writeFileSync(join(root, "home", ".hypomnema", "client.json"),
    JSON.stringify({ url: `http://127.0.0.1:${address.port}/`, token: "hyp_bot_v1_pi" }));
  mkdirSync(join(root, "tmp"));
  const before = { HOME: process.env.HOME, TMPDIR: process.env.TMPDIR };
  process.env.HOME = join(root, "home");
  process.env.TMPDIR = join(root, "tmp");
  // An unset variable is deleted again: assigning undefined to process.env stores the string "undefined".
  t.after(() => {
    for (const [name, value] of Object.entries(before)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  });
  return { root, reports, auth, end };
}

test("a background command that ends is reported once, with its exit code and output", async (t) => {
  const cp = await controlPlane(t);
  const { id } = await startBackground("echo made; echo warned >&2; exit 3", cp.root);

  assert.deepEqual(cp.reports, [{ kind: "started", id }], "it was recorded before it ran");
  const end = await cp.end;
  assert.deepEqual(end, { kind: "ended", id, exit_code: 3, output: "made\nwarned\n", output_chars: 12 });
  assert.deepEqual(cp.auth, ["Bearer hyp_bot_v1_pi", "Bearer hyp_bot_v1_pi"], "pi's own token reports");
  assert.equal(existsSync(join(cp.root, "tmp", `pi-background-${id}.log`)), false,
    "a file whose output the wake has whole is not kept");
});

test("a long output's wake has its last characters and its file", async (t) => {
  const cp = await controlPlane(t);
  const { id } = await startBackground("i=0; while [ $i -lt 600 ]; do printf 'line %03d\\n' $i; i=$((i+1)); done", cp.root);

  const end = await cp.end;
  const all = Array.from({ length: 600 }, (_, i) => `line ${String(i).padStart(3, "0")}\n`).join("");
  assert.equal(end.exit_code, 0);
  assert.equal(end.output, all.slice(all.length - TAIL_CHARS));
  assert.equal(end.output_chars, all.length);
  assert.equal(end.output_file, join(cp.root, "tmp", `pi-background-${id}.log`));
  assert.equal(end.dropped, false);
  assert.equal(readFileSync(String(end.output_file), "utf8"), all);
});

test("a command killed in the background is reported killed", async (t) => {
  const cp = await controlPlane(t);
  const { pid } = await startBackground("echo up; sleep 30", cp.root);
  await new Promise((r) => setTimeout(r, 200));
  process.kill(-pid, "SIGTERM");

  const end = await cp.end;
  assert.equal(end.signal, "SIGTERM");
  assert.equal(end.exit_code, undefined);
});

test("a background command has OpenClaw's deadline of 1,800 s unless it asks for another", async (t) => {
  const cp = await controlPlane(t);
  assert.equal(DEFAULT_TIMEOUT_SECONDS, 1800);

  const started = await startBackground("sleep 30", cp.root);
  assert.equal(started.timeoutSeconds, 1800);
  process.kill(-started.pid, "SIGKILL");
  await cp.end;
});

test("its timeout kills it, and the wake says its time ran out", async (t) => {
  const cp = await controlPlane(t);
  const started = await startBackground("sleep 30", cp.root, { timeout: 0.2 });
  assert.equal(started.timeoutSeconds, 0.2);

  const end = await cp.end;
  assert.equal(end.signal, "SIGTERM");
  assert.equal(end.timed_out, true);
});

test("a command that ignores SIGTERM is killed when its time runs out", async (t) => {
  const cp = await controlPlane(t);
  await startBackground("trap '' TERM; sleep 30", cp.root, { timeout: 0.2, killGraceMs: 200 });

  const end = await cp.end;
  assert.equal(end.signal, "SIGKILL", "SIGKILL follows SIGTERM after the grace");
  assert.equal(end.timed_out, true);
});

test("output a child writes after the end was reported does not end pi", async (t) => {
  const cp = await controlPlane(t);
  await startBackground("(sleep 0.5; echo late) & echo early", cp.root, { pipeGraceMs: 100 });

  const end = await cp.end;
  assert.equal(end.exit_code, 0);
  assert.equal(end.output, "early\n");
  // Writing to the closed file would throw in this process, failing the test.
  await new Promise((r) => setTimeout(r, 1000));
});

test("a character split between two reads is kept whole", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "pi-background-utf8-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const output = new Output(join(dir, "out.log"));

  output.write(Buffer.from([0xc3]));
  output.write(Buffer.from([0xa9]));
  output.close();

  assert.equal(output.tail, "é");
  assert.equal(output.chars, 1);
  assert.equal(readFileSync(join(dir, "out.log"), "utf8"), "é");
});

test("a command the control plane does not record is not started", async (t) => {
  const cp = await controlPlane(t, 403);
  const marker = join(cp.root, "ran");

  await assert.rejects(startBackground(`touch ${marker}`, cp.root), /answered 403/);
  await new Promise((r) => setTimeout(r, 200));
  assert.equal(existsSync(marker), false);
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

test("pinned pi declares bash with background and runs a call of it in the background", { timeout: 20_000 }, async (t) => {
  const cp = await controlPlane(t);
  const requests = await piCallingBash(t, { command: "echo from-pi", background: true });

  const tools = JSON.parse(requests[0]).tools as { function: { name: string; description: string; parameters: any } }[];
  const bash = tools.find((tool) => tool.function.name === "bash");
  assert.ok(bash, "bash is declared");
  assert.equal(bash.function.parameters.properties.background.type, "boolean");
  assert.ok(bash.function.parameters.properties.command, "pi's own parameters stay");
  assert.match(bash.function.description, /Do not sleep, poll its log/);

  assert.match(toolResult(requests[1]), /Started in the background as bg_[0-9a-f]{12}/);
  assert.match(toolResult(requests[1]), /it is killed if it runs longer than 30m/);
  const end = await cp.end;
  assert.equal(end.exit_code, 0);
  assert.equal(end.output, "from-pi\n");
});

test("bash keeps pi's shell settings, in the foreground and in the background", { timeout: 30_000 }, async (t) => {
  const settings = { shellCommandPrefix: "echo configured-prefix" };

  const foreground = await piCallingBash(t, { command: "echo body" }, settings);
  assert.match(toolResult(foreground[1]), /configured-prefix\\nbody/);

  const cp = await controlPlane(t);
  await piCallingBash(t, { command: "echo body", background: true }, settings);
  assert.equal((await cp.end).output, "configured-prefix\nbody\n");
});
