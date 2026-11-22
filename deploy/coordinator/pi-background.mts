// pi's bash with a background mode (issue #1058). With `background: true` the command starts in the box and the tool
// answers at once; when it ends, the control plane wakes this chat with its exit code, how long it ran and the last
// 2,000 characters of its output, so the Bot ends its turn instead of sleeping to poll, as OpenClaw's exec wakes the
// conversation when a backgrounded command exits (docs/tools/exec.md:171-175, src/agents/bash-tools.exec-notify.ts:
// 216-260), opencode's shell answers "You will be notified when it completes. DO NOT poll" and Claude Code's
// run_in_background re-invokes the agent. pi's own bash has no such mode (core/tools/bash.ts); without `background`
// this is pi's bash, made with pi's shell settings (agent-session.ts:3766-3778).
//
// The extension tells the control plane, with pi's own token from the box's client.json, when a command starts and
// when it ends (agenttool.PathBackground). A command that cannot be recorded is not started: nothing would wake the
// chat, and an idle pi would be stopped with its box. A command has a deadline, OpenClaw's 1,800 s unless the call
// gives its own (exec.md:192); then it gets SIGTERM and, 5 s later, SIGKILL, as OpenClaw stops a process
// (src/process/supervisor/released-processes.ts:97-125, cancellation-policy.ts:4), so its end always comes and its
// pi is never kept for it forever. All its output goes to a file of the box's /tmp, kept only when the wake leaves
// part of it out, at most 64 MiB: OpenClaw's Gateway-host file (exec.md:173-174); keeping it in the box is this
// extension's choice.
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { closeSync, openSync, readFileSync, rmSync, writeSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { StringDecoder } from "node:string_decoder";
import {
  createBashToolDefinition,
  getShellConfig,
  type ExtensionAPI,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

/** The end of the output a wake carries, in characters (OpenClaw's tail, bash-tools.exec-notify.ts:234). */
export const TAIL_CHARS = 2000;
/** Output past this many bytes is not written to the file (OpenClaw's hard limit, exec.md:174). */
export const FILE_LIMIT = 64 << 20;
/** A command's deadline when its call gives none (OpenClaw's tools.exec.timeoutSeconds, exec.md:192). */
export const DEFAULT_TIMEOUT_SECONDS = 1800;
/** The longest deadline, pi's bash's own (core/tools/bash.ts:21-24): Node's longest timer. */
const MAX_TIMEOUT_SECONDS = 2_147_483_647 / 1000;
/** How long SIGTERM has before SIGKILL (OpenClaw's GRACEFUL_CANCEL_TIMEOUT_MS). */
const KILL_GRACE_MS = 5000;
/** How long a command that exited may keep its pipes open before it is reported (nodeexec's and Coder's 5 s). */
const PIPE_GRACE_MS = 5000;
const PATH_BACKGROUND = "/agent/v1/background";

export const backgroundGuidance =
  " Set background to true for a command that may take longer than about a minute, such as a download, a build or " +
  "a long script: it answers at once with the command's id and the command runs on, killed after its timeout " +
  `(${DEFAULT_TIMEOUT_SECONDS} seconds when not given). When it ends, this chat is woken with its exit code, how ` +
  "long it ran and the last 2,000 characters of its output, so end your turn if only waiting is left. Do not sleep, " +
  "poll its log or start it again to wait for it, and do not put it in the background yourself with & or nohup: " +
  "nothing would wake you.";

type Client = { url: string; token: string };

type Report =
  | { kind: "started"; id: string }
  | {
      kind: "ended";
      id: string;
      exit_code?: number;
      signal?: string;
      timed_out?: boolean;
      output?: string;
      output_chars?: number;
      output_file?: string;
      dropped?: boolean;
    };

/** How one command runs: its deadline in seconds, and pi's shell settings. The graces are for tests. */
export type BackgroundOptions = {
  timeout?: number;
  shellPath?: string;
  commandPrefix?: string;
  killGraceMs?: number;
  pipeGraceMs?: number;
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

/**
 * Keeps the end of a command's output and writes all of it, up to FILE_LIMIT, to its file. Characters are read
 * across chunks, so one split between two reads stays whole. Output after close, from a child that kept the pipes,
 * is let go of, and a file that cannot be written only stops being written: neither ends pi.
 */
export class Output {
  tail = "";
  chars = 0;
  bytes = 0;
  dropped = false;
  readonly path: string;
  private fd: number | undefined;
  private decoder = new StringDecoder("utf8");
  private closed = false;

  constructor(path: string) {
    this.path = path;
    this.fd = openSync(path, "wx", 0o600);
  }

  write(chunk: Buffer) {
    if (this.closed) return;
    this.take(this.decoder.write(chunk));
    const room = FILE_LIMIT - this.bytes;
    if (room <= 0 || this.fd === undefined) {
      this.dropped = true;
      return;
    }
    const part = chunk.length > room ? chunk.subarray(0, room) : chunk;
    if (part.length < chunk.length) this.dropped = true;
    try {
      writeSync(this.fd, part);
      this.bytes += part.length;
    } catch {
      this.dropped = true;
      this.closeFile();
    }
  }

  close() {
    if (this.closed) return;
    this.take(this.decoder.end());
    this.closed = true;
    this.closeFile();
  }

  private take(text: string) {
    if (!text) return;
    this.chars += [...text].length;
    this.tail = lastChars(this.tail + text, TAIL_CHARS);
  }

  private closeFile() {
    if (this.fd === undefined) return;
    try {
      closeSync(this.fd);
    } catch {}
    this.fd = undefined;
  }
}

export function lastChars(text: string, n: number): string {
  const chars = [...text];
  return chars.length <= n ? text : chars.slice(chars.length - n).join("");
}

/** A deadline as people read it: 30m, 90s, 1h30m. */
export function duration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds * 10) / 10}s`;
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.round(seconds % 60);
  return (h ? `${h}h` : "") + (m ? `${m}m` : "") + (s ? `${s}s` : "");
}

function deadline(timeout: number | undefined): number {
  if (timeout === undefined) return DEFAULT_TIMEOUT_SECONDS;
  if (!Number.isFinite(timeout) || timeout <= 0 || timeout > MAX_TIMEOUT_SECONDS) {
    throw new Error(`Invalid timeout: give seconds above 0, at most ${MAX_TIMEOUT_SECONDS}`);
  }
  return timeout;
}

/** Runs one command in the background and reports its end; resolves once it was recorded and started. */
export async function startBackground(
  command: string,
  cwd: string,
  options: BackgroundOptions = {},
): Promise<{ id: string; pid: number; timeoutSeconds: number }> {
  const timeoutSeconds = deadline(options.timeout);
  const c = client();
  const id = "bg_" + randomBytes(6).toString("hex");
  await report(c, { kind: "started", id });

  const output = new Output(join(tmpdir(), `pi-background-${id}.log`));
  const { shell, args } = getShellConfig(options.shellPath);
  const script = options.commandPrefix ? `${options.commandPrefix}\n${command}` : command;
  const child = spawn(shell, [...args, script], {
    cwd, detached: true, stdio: ["ignore", "pipe", "pipe"], env: process.env,
  });
  child.stdout.on("data", (b: Buffer) => output.write(b));
  child.stderr.on("data", (b: Buffer) => output.write(b));

  const signalGroup = (signal: NodeJS.Signals) => {
    try {
      if (child.pid) process.kill(-child.pid, signal);
    } catch {}
  };
  let timedOut = false;
  let killTimer: NodeJS.Timeout | undefined;
  const timer = setTimeout(() => {
    timedOut = true;
    signalGroup("SIGTERM");
    // SIGKILL follows whether the shell exited or not: what it started in its group goes too.
    killTimer = setTimeout(() => signalGroup("SIGKILL"), options.killGraceMs ?? KILL_GRACE_MS);
  }, timeoutSeconds * 1000);

  const ended = new Promise<{ code: number | null; signal: NodeJS.Signals | null; spawnError?: Error }>((resolve) => {
    child.once("error", (err) => resolve({ code: 127, signal: null, spawnError: err }));
    child.once("exit", (code, signal) => {
      // A child it left running may hold the pipes open; the exit is reported after a short wait for them.
      const grace = setTimeout(() => resolve({ code, signal }), options.pipeGraceMs ?? PIPE_GRACE_MS);
      child.once("close", () => {
        clearTimeout(grace);
        resolve({ code, signal });
      });
    });
  });

  void ended.then(async ({ code, signal, spawnError }) => {
    clearTimeout(timer);
    if (!timedOut && killTimer) clearTimeout(killTimer);
    if (spawnError) output.write(Buffer.from(`starting the command failed: ${spawnError.message}\n`));
    output.close();
    const cut = output.chars > [...output.tail].length;
    if (!cut) rmSync(output.path, { force: true });
    const body: Report = {
      kind: "ended", id, output: output.tail, output_chars: output.chars,
      ...(signal ? { signal } : { exit_code: code ?? 1 }),
      ...(timedOut ? { timed_out: true } : {}),
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
  return { id, pid: child.pid, timeoutSeconds };
}

export default function background(pi: ExtensionAPI) {
  const cwd = process.cwd();
  const schema = createBashToolDefinition(cwd);
  // pi's own bash, made as pi makes it, with the shell settings read at each call.
  const shell = () => {
    const settings = pi.getSettings();
    return { commandPrefix: settings.shellCommandPrefix, shellPath: settings.shellPath };
  };

  pi.registerTool({
    ...schema,
    description: schema.description + backgroundGuidance,
    parameters: Type.Object({
      ...schema.parameters.properties,
      background: Type.Optional(
        Type.Boolean({
          description: `Run it in the background; its end wakes this chat. It is killed after its timeout, ` +
            `${DEFAULT_TIMEOUT_SECONDS} seconds when not given.`,
        }),
      ),
    }),
    async execute(id, input, signal, update, ctx) {
      const { background: inBackground, ...rest } = input as { command: string; timeout?: number; background?: boolean };
      if (!inBackground) return createBashToolDefinition(cwd, shell()).execute(id, rest, signal, update, ctx);

      const started = await startBackground(rest.command, ctx?.cwd || cwd, { timeout: rest.timeout, ...shell() });
      const text =
        `Started in the background as ${started.id} (process group ${started.pid}); it is killed if it runs longer ` +
        `than ${duration(started.timeoutSeconds)}. When it ends, the control plane wakes this chat with its exit ` +
        "code, how long it ran and the last 2,000 characters of its output. If only waiting is left, end your turn " +
        `now: do not sleep, poll or start it again. To stop it: kill -TERM -${started.pid}`;
      return {
        content: [{ type: "text", text }],
        details: undefined,
        structuredContent: { output: text, truncated: false, exit_code: 0, wall_time_seconds: 0 },
      };
    },
  });
}
