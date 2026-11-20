import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
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

test("pinned pi loads the confined notes and refuses the RPC shell", { timeout: 20_000 }, async (test) => {
  const cwd = mkdtempSync(join(tmpdir(), "pi-read-run-"));
  test.after(() => rmSync(cwd, { recursive: true, force: true }));
  const pi = spawn(resolve(import.meta.dirname, "node_modules/.bin/pi"), [
    "--mode", "rpc", "--offline", "--no-session", "--no-approve", "--no-extensions", "--no-skills",
    "--no-context-files", "--no-prompt-templates", "--extension", resolve(import.meta.dirname, "pi-read-run.mts"),
    "--tools", "routine_read,routine_write,routine_edit,mcp__hypomnema__*",
  ], { cwd, env: { ...process.env, PI_CODING_AGENT_DIR: join(cwd, "agent") } });
  test.after(() => pi.kill());
  const lines = createInterface({ input: pi.stdout });
  test.after(() => lines.close());
  let stderr = "";
  pi.stderr.on("data", (chunk) => { stderr += String(chunk); });
  pi.stdin.write(JSON.stringify({ id: "shell", type: "bash", command: "echo shell-ran" }) + "\n");
  for await (const line of lines) {
    const reply = JSON.parse(line);
    if (reply.id !== "shell") continue;
    assert.equal(reply.success, true);
    assert.equal(reply.data.exitCode, 1);
    assert.match(reply.data.output, /This run reads only/);
    return;
  }
  assert.fail(`pi exited without an answer: ${stderr}`);
});
