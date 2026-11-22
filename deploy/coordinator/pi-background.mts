// pi's bash with a background mode (issues #1058, #1084). With `background: true` the control plane starts the
// command as a systemd user unit of its own, outside pi's box and outside the control plane's service, so a restart
// of the control plane, which ends pi, leaves it running; the tool answers at once, and when the command ends the
// control plane wakes this chat with its exit code, how long it ran and the last 2,000 characters of its output, so
// the Bot ends its turn instead of sleeping to poll. OpenClaw's durable exec runs a command the same way, as a
// transient systemd user service that survives Gateway restarts (docs/tools/exec.md:94-100), and its exec wakes the
// conversation when a backgrounded command exits (docs/tools/exec.md:171-175); opencode's shell answers "You will be
// notified when it completes. DO NOT poll" and Claude Code's run_in_background re-invokes the agent. pi's own bash has
// no such mode (core/tools/bash.ts); without `background` this is pi's bash, made with pi's shell settings
// (agent-session.ts:3766-3778).
//
// The extension asks with pi's own token from the box's client.json (agenttool.PathBackground); the box cannot reach
// the user manager itself, which would let a command run outside any box. A command has a deadline, OpenClaw's
// 1,800 s unless the call gives its own (exec.md:192), after which systemd stops it (RuntimeMaxSec, as OpenClaw maps
// timeoutSeconds); its output goes to a file of this chat's run folder, which this box reads. It gets no Hypomnema
// token: pi's ends with pi, as OpenClaw's durable commands launch without the Gateway's credentials (exec.md:94).
// `background_stop` stops one, as OpenClaw's `process kill` stops a durable unit (background-process.md:165).
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import {
  createBashToolDefinition,
  type ExtensionAPI,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

/** A command's deadline when its call gives none (OpenClaw's tools.exec.timeoutSeconds, exec.md:192). */
export const DEFAULT_TIMEOUT_SECONDS = 1800;
/** The longest deadline, pi's bash's own (core/tools/bash.ts:21-24). */
const MAX_TIMEOUT_SECONDS = 2_147_483;
const PATH_BACKGROUND = "/agent/v1/background";

export const backgroundGuidance =
  " Set background to true for a command that may take longer than about a minute, such as a download, a build, a " +
  "long script or a watcher: it answers at once with the command's id and the command runs on, outside this " +
  "session, so a restart of the control plane does not stop it; it is killed after its timeout " +
  `(${DEFAULT_TIMEOUT_SECONDS} seconds when not given; give a longer one for a watcher). When it ends, this chat is ` +
  "woken with its exit code, how long it ran and the last 2,000 characters of its output, so end your turn if only " +
  "waiting is left. It runs in your directory with your tools, but with no Hypomnema login: run what needs " +
  "Hypomnema's tools in the foreground. Do not sleep, poll its log or start it again to wait for it, and do not put " +
  "it in the background yourself with &, setsid or nohup: a restart stops that, and nothing would wake you. Stop " +
  "one with background_stop.";

type Client = { url: string; token: string };

/** What the control plane answers: the command's id, its output file and its deadline; for a stop, whether it ran. */
export type Answer = { id: string; output_file?: string; timeout_seconds?: number; stopped?: boolean };

function client(): Client {
  const raw = JSON.parse(readFileSync(join(homedir(), ".hypomnema", "client.json"), "utf8"));
  if (typeof raw?.url !== "string" || typeof raw?.token !== "string") throw new Error("client.json has no url and token");
  return { url: raw.url.replace(/\/+$/, ""), token: raw.token };
}

async function ask(body: object): Promise<Answer> {
  const c = client();
  const res = await fetch(c.url + PATH_BACKGROUND, {
    method: "POST",
    headers: { Authorization: `Bearer ${c.token}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) {
    let message = text.slice(0, 500);
    try {
      message = JSON.parse(text).message ?? message;
    } catch {}
    throw new Error(`the control plane answered ${res.status}: ${message}`);
  }
  return JSON.parse(text) as Answer;
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
  return Math.ceil(timeout);
}

/** Asks the control plane to start one command in the background, with pi's shell prefix before it. */
export async function startBackground(
  command: string,
  cwd: string,
  options: { timeout?: number; commandPrefix?: string } = {},
): Promise<{ id: string; outputFile: string; timeoutSeconds: number }> {
  const timeoutSeconds = deadline(options.timeout);
  const script = options.commandPrefix ? `${options.commandPrefix}\n${command}` : command;
  const got = await ask({ kind: "start", command: script, cwd, timeout_seconds: timeoutSeconds });
  return { id: got.id, outputFile: got.output_file ?? "", timeoutSeconds: got.timeout_seconds ?? timeoutSeconds };
}

/** Asks the control plane to stop a command; false when it had ended already. */
export async function stopBackground(id: string): Promise<boolean> {
  return (await ask({ kind: "stop", id })).stopped === true;
}

function text(t: string) {
  return {
    content: [{ type: "text" as const, text: t }],
    details: undefined,
    structuredContent: { output: t, truncated: false, exit_code: 0, wall_time_seconds: 0 },
  };
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
          description: "Run it in the background, where a restart of the control plane does not stop it; its end " +
            `wakes this chat. It is killed after its timeout, ${DEFAULT_TIMEOUT_SECONDS} seconds when not given.`,
        }),
      ),
    }),
    async execute(id, input, signal, update, ctx) {
      const { background: inBackground, ...rest } = input as { command: string; timeout?: number; background?: boolean };
      if (!inBackground) return createBashToolDefinition(cwd, shell()).execute(id, rest, signal, update, ctx);

      const started = await startBackground(rest.command, ctx?.cwd || cwd, {
        timeout: rest.timeout, commandPrefix: shell().commandPrefix,
      });
      return text(
        `Started in the background as ${started.id}; it is killed if it runs longer than ` +
          `${duration(started.timeoutSeconds)}, and a restart of the control plane does not stop it. Its output goes ` +
          `to ${started.outputFile}. When it ends, the control plane wakes this chat with its exit code, how long it ` +
          "ran and the last 2,000 characters of its output. If only waiting is left, end your turn now: do not " +
          `sleep, poll or start it again. To stop it: background_stop with id ${started.id}.`,
      );
    },
  });

  pi.registerTool({
    name: "background_stop",
    label: "background_stop",
    description: "Stop a command you started with bash's background: true, by its id (bg_…): it gets SIGTERM, and " +
      "SIGKILL 5 seconds later. This chat is then woken with what it wrote. Use it when its work is no longer needed.",
    parameters: Type.Object({
      id: Type.String({ description: "The command's id, as bash answered it, such as bg_0123456789ab." }),
    }),
    async execute(_id, input) {
      const { id } = input as { id: string };
      return text(await stopBackground(id)
        ? `Stopped ${id}; this chat is woken with what it wrote.`
        : `${id} was not running: it had ended, and its end wakes this chat if it has not yet.`);
    },
  });
}
