// pi's bash with a background mode (issue #1058). With `background: true` the command starts in the box and the tool
// answers at once; when it ends, the control plane wakes this chat with its exit code, how long it ran and the last
// 2,000 characters of its output, so the Bot ends its turn instead of sleeping to poll, as OpenClaw's exec wakes the
// conversation when a backgrounded command exits (docs/tools/exec.md:176-182, src/agents/bash-tools.exec-notify.ts),
// opencode's shell answers "You will be notified when it completes. DO NOT poll" and Claude Code's run_in_background
// re-invokes the agent. pi's own bash has no such mode (core/tools/bash.ts); without `background` this is pi's bash.
//
// The extension tells the control plane, with pi's own token from the box's client.json, when a command starts and
// when it ends (agenttool.PathBackground). A command that cannot be recorded is not started: nothing would wake the
// chat, and an idle pi would be stopped with its box. All output goes to a file of the box's /tmp, which is pi's
// own, kept only when the wake leaves part of it out, and at most 64 MiB, after OpenClaw's (exec.md:182-183).
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { closeSync, openSync, readFileSync, rmSync, writeSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import {
  createBashToolDefinition,
  getShellConfig,
  type ExtensionAPI,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

/** The end of the output a wake carries, in characters (OpenClaw's tail, bash-tools.exec-notify.ts:234). */
export const TAIL_CHARS = 2000;
/** Output past this many bytes is not written to the file (OpenClaw's hard limit, exec.md:183). */
export const FILE_LIMIT = 64 << 20;
/** How long a command that exited may keep its pipes open before it is reported (nodeexec's and Coder's 5 s). */
const PIPE_GRACE_MS = 5000;
const PATH_BACKGROUND = "/agent/v1/background";

export const backgroundGuidance =
  " Set background to true for a command that may take longer than about a minute, such as a download, a build or " +
  "a long script: it answers at once with the command's id and the command runs on. When it ends, this chat is " +
  "woken with its exit code, how long it ran and the last 2,000 characters of its output, so end your turn if only " +
  "waiting is left. Do not sleep, poll its log or start it again to wait for it, and do not put it in the " +
  "background yourself with & or nohup: nothing would wake you.";

type Client = { url: string; token: string };

type Report =
  | { kind: "started"; id: string }
  | {
      kind: "ended";
      id: string;
      exit_code?: number;
      signal?: string;
      output?: string;
      output_chars?: number;
      output_file?: string;
      dropped?: boolean;
    };

function client(): Client {
  const raw = JSON.parse(readFileSync(join(homedir(), ".hypomnema", "client.json"), "utf8"));
  if (typeof raw?.url !== "string" || typeof raw?.token !== "string") throw new Error("client.json has no url and token");
  return { url: raw.url.replace(/\/+$/, ""), token: raw.token };
}

async function report(c: Client, body: Report): Promise<void> {
  const res = await fetch(c.url + PATH_BACKGROUND, {
    method: "POST",
    headers: { Authorization: `Bearer ${c.token}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`the control plane answered ${res.status}: ${(await res.text()).slice(0, 300)}`);
}

/** Keeps the end of a command's output and writes all of it, up to FILE_LIMIT, to its file. */
export class Output {
  tail = "";
  chars = 0;
  bytes = 0;
  dropped = false;
  readonly path: string;
  private fd: number;

  constructor(path: string) {
    this.path = path;
    this.fd = openSync(path, "wx", 0o600);
  }

  write(chunk: Buffer) {
    const text = chunk.toString("utf8");
    this.chars += [...text].length;
    this.tail = lastChars(this.tail + text, TAIL_CHARS);
    const room = FILE_LIMIT - this.bytes;
    if (room <= 0) {
      this.dropped = true;
      return;
    }
    const part = chunk.length > room ? chunk.subarray(0, room) : chunk;
    if (part.length < chunk.length) this.dropped = true;
    writeSync(this.fd, part);
    this.bytes += part.length;
  }

  close() {
    closeSync(this.fd);
  }
}

export function lastChars(text: string, n: number): string {
  const chars = [...text];
  return chars.length <= n ? text : chars.slice(chars.length - n).join("");
}

/** Runs one command in the background and reports its end; resolves once it was recorded and started. */
export async function startBackground(command: string, cwd: string, timeout?: number): Promise<{ id: string; pid: number }> {
  const c = client();
  const id = "bg_" + randomBytes(6).toString("hex");
  await report(c, { kind: "started", id });

  const output = new Output(join(tmpdir(), `pi-background-${id}.log`));
  const { shell, args } = getShellConfig();
  const child = spawn(shell, [...args, command], {
    cwd, detached: true, stdio: ["ignore", "pipe", "pipe"], env: process.env,
  });
  child.stdout.on("data", (b: Buffer) => output.write(b));
  child.stderr.on("data", (b: Buffer) => output.write(b));

  const killGroup = () => {
    try {
      if (child.pid) process.kill(-child.pid, "SIGTERM");
    } catch {}
  };
  const timer = timeout && timeout > 0 ? setTimeout(killGroup, timeout * 1000) : undefined;

  const ended = new Promise<{ code: number | null; signal: NodeJS.Signals | null; spawnError?: Error }>((resolve) => {
    child.once("error", (err) => resolve({ code: 127, signal: null, spawnError: err }));
    child.once("exit", (code, signal) => {
      // A child it left running may hold the pipes open; the exit is reported after a short wait for them.
      const grace = setTimeout(() => resolve({ code, signal }), PIPE_GRACE_MS);
      child.once("close", () => {
        clearTimeout(grace);
        resolve({ code, signal });
      });
    });
  });

  void ended.then(async ({ code, signal, spawnError }) => {
    if (timer) clearTimeout(timer);
    if (spawnError) output.write(Buffer.from(`starting the command failed: ${spawnError.message}\n`));
    output.close();
    const cut = output.chars > [...output.tail].length;
    if (!cut) rmSync(output.path, { force: true });
    const body: Report = {
      kind: "ended", id, output: output.tail, output_chars: output.chars,
      ...(signal ? { signal } : { exit_code: code ?? 1 }),
      ...(cut ? { output_file: output.path, dropped: output.dropped } : {}),
    };
    for (let attempt = 0; ; attempt++) {
      try {
        await report(c, body);
        return;
      } catch (err) {
        if (attempt >= 4) {
          console.error(`pi-background: the end of ${id} could not be reported: ${err}`);
          return;
        }
        await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)));
      }
    }
  });

  if (!child.pid) {
    await ended;
    throw new Error("the command could not be started");
  }
  return { id, pid: child.pid };
}

export default function background(pi: ExtensionAPI) {
  const cwd = process.cwd();
  const bash = createBashToolDefinition(cwd);

  pi.registerTool({
    ...bash,
    description: bash.description + backgroundGuidance,
    parameters: Type.Object({
      ...bash.parameters.properties,
      background: Type.Optional(
        Type.Boolean({ description: "Run it in the background; its end wakes this chat. Its timeout, when given, still kills it." }),
      ),
    }),
    async execute(id, input, signal, update, ctx) {
      const { background: inBackground, ...rest } = input as { command: string; timeout?: number; background?: boolean };
      if (!inBackground) return bash.execute(id, rest, signal, update, ctx);

      const started = await startBackground(rest.command, ctx?.cwd || cwd, rest.timeout);
      const text =
        `Started in the background as ${started.id} (process group ${started.pid}). When it ends, the control plane ` +
        "wakes this chat with its exit code, how long it ran and the last 2,000 characters of its output. If only " +
        "waiting is left, end your turn now: do not sleep, poll or start it again. To stop it: " +
        `kill -TERM -${started.pid}`;
      return {
        content: [{ type: "text", text }],
        details: undefined,
        structuredContent: { output: text, truncated: false, exit_code: 0, wall_time_seconds: 0 },
      };
    },
  });
}
