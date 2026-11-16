// Writes dist/LICENSES.txt: the license of every package the bundle carries (zca-js and what it needs), in full,
// as their licenses ask a copy to keep them. It is embedded beside the bundle (embed.go).
import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// Run by `pnpm build`, which names itself in npm_execpath, so that no shell is needed to find it.
const pnpm = process.env.npm_execpath;
if (!pnpm) throw new Error("run it with pnpm build");

// pnpm is a script Node runs, or a program of its own (pnpm's standalone build).
const args = ["licenses", "list", "--prod", "--json", "--long"];
const [cmd, argv] = /\.[cm]?js$/.test(pnpm) ? [process.execPath, [pnpm, ...args]] : [pnpm, args];
const list = JSON.parse(execFileSync(cmd, argv, { encoding: "utf8" }));

const parts = [];

for (const [license, packages] of Object.entries(list)) {
  for (const p of packages) {
    for (const [i, version] of (p.versions ?? []).entries()) {
      const dir = p.paths?.[i];
      const file = dir && readdirSync(dir).find((f) => /^(licen[cs]e|copying)(\.|$)/i.test(f));
      const text = file ? readFileSync(join(dir, file), "utf8").trim() : `${license}; no license file is shipped.`;
      parts.push(`${p.name} ${version} (${license})\n\n${text}`);
    }
  }
}

parts.sort();
writeFileSync("dist/LICENSES.txt",
  "Packages bundled into the Zalo bridge (bridges/zalo), with their licenses.\n\n" +
  parts.join("\n\n" + "-".repeat(78) + "\n\n") + "\n");
