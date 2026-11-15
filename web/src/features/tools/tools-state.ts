import type { ToolFamily } from "@/api/generated/types.gen"

// What a profile's or a Bot's tool switches mean (issue #536, step 7), as the control plane reads them
// (internal/shared/agenttool/switches.go): an entry is a tool's name, or a family, a name's start and "*"; tools
// keeps only what it names when it names any, and tools_off wins over it.

export function matches(entry: string, tool: string) {
  return entry.endsWith("*") ? tool.startsWith(entry.slice(0, -1)) : entry === tool
}

// kept says whether the allow list leaves any tool of the family: an empty list keeps every one.
export function kept(family: Pick<ToolFamily, "tools">, tools: string[]) {
  return tools.length === 0 || tools.some((e) => family.tools.some((t) => matches(e, t)))
}

// familyOn says whether the family's switch shows on: the allow list keeps it and its own entry is not off.
export function familyOn(
  family: Pick<ToolFamily, "tools" | "pattern">,
  tools: string[],
  off: string[],
) {
  return kept(family, tools) && !off.includes(family.pattern)
}

// others are the entries a family's switch does not set: single names and families other than the ones listed,
// which the YAML or the CLI set and the form keeps as they are.
export function others(families: Pick<ToolFamily, "pattern">[], entries: string[]) {
  const patterns = new Set(families.map((f) => f.pattern))

  return entries.filter((e) => !patterns.has(e))
}
