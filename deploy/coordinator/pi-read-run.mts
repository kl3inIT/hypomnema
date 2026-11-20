import { lstatSync } from "node:fs";
import { relative, resolve } from "node:path";
import {
  createEditToolDefinition,
  createReadToolDefinition,
  createWriteToolDefinition,
  type ExtensionAPI,
  type ToolDefinition,
} from "@earendil-works/pi-coding-agent";
import type { TSchema } from "typebox";

export function notePath(cwd: string, path: string): string | undefined {
  if (!/^[a-zA-Z0-9_./-]+$/.test(path)) return;

  const absolute = resolve(cwd, path);
  const within = relative(cwd, absolute);
  if (!within || within === ".." || within.startsWith("../")) return;

  let parent = cwd;
  for (const segment of within.split("/")) {
    parent = resolve(parent, segment);
    const stat = lstatSync(parent, { throwIfNoEntry: false });
    if (!stat) break;
    if (parent !== absolute ? !stat.isDirectory() : !stat.isFile()) return;
  }

  return absolute;
}

export default function readRun(pi: ExtensionAPI) {
  const cwd = process.cwd();

  pi.on("user_bash", () => ({
    result: { output: "This run reads only; do not try another way.", exitCode: 1, cancelled: false, truncated: false },
  }));

  function register<Params extends TSchema, Details>(tool: ToolDefinition<Params, Details>) {
    pi.registerTool({
      ...tool,
      name: `routine_${tool.name}`,
      execute: (id, input, signal, update, ctx) => {
        const asked = (input as { path?: unknown }).path;
        const path = typeof asked === "string" && notePath(cwd, asked);
        if (!path) throw new Error("This run reads only outside its own notes; do not try another way.");
        return tool.execute(id, { ...input, path }, signal, update, ctx);
      },
    });
  }

  register(createReadToolDefinition(cwd));
  register(createWriteToolDefinition(cwd));
  register(createEditToolDefinition(cwd));
}
