import type { Node } from "@/api/generated/types.gen"

// What the Machines page says about a node, worked out from GET /nodes.

export type MachineState =
  | "online"
  | "drained"
  | "offline"
  | "creating"
  | "create_failed"
  | "removing"
  | "remove_failed"

// What a provider's machine is doing, when that says more than its connection: being created, not created, being
// destroyed, or not destroyed yet.
const providerStates: Partial<Record<NonNullable<Node["machine"]>["state"], MachineState>> = {
  provisioning: "creating",
  provision_failed: "create_failed",
  deleting: "removing",
  delete_failed: "remove_failed",
}

// machineState: a provider's machine that is not ready says where it is; otherwise a connected node takes work
// (online) or was drained by the owner, and a node that is not connected is offline whatever its status.
export function machineState(node: Node): MachineState {
  const provider = node.machine && providerStates[node.machine.state]
  if (provider) return provider

  if (!node.connected) return "offline"

  return node.status === "drained" ? "drained" : "online"
}

export const stateNames: Record<MachineState, string> = {
  online: "Online",
  drained: "Drained",
  offline: "Offline",
  creating: "Creating",
  create_failed: "Not created",
  removing: "Removing",
  remove_failed: "Not removed",
}

// machineProblem is why a provider's machine was not created or not destroyed, in the provider's words.
export function machineProblem(node: Node) {
  const state = machineState(node)

  return state === "create_failed" || state === "remove_failed" ? node.machine?.error : undefined
}

export type MeterTone = "good" | "warn" | "crit"

// meterTone is Beszel's getMeterStateByThresholds (systems-table-columns.tsx): a meter warns from 65 % and is
// critical from 90 %.
export function meterTone(value: number, warn = 65, crit = 90): MeterTone {
  return value >= crit ? "crit" : value >= warn ? "warn" : "good"
}

// percent is used of total as 0 to 100, or undefined when either is unknown.
export function percent(used: number | undefined, total: number | undefined) {
  if (used === undefined || !total) return undefined

  return Math.min(100, (100 * used) / total)
}

export const memoryPercent = (node: Node) =>
  percent(node.stats?.memory_used_bytes, node.system?.memory_bytes)

export const diskPercent = (node: Node) =>
  percent(node.stats?.disk_used_bytes, node.system?.disk_bytes)

// loadPercent is the one-minute load against the machine's CPUs, which colours the load's dot as Beszel does.
export function loadPercent(node: Node) {
  if (!node.stats) return undefined

  return (100 * node.stats.load1) / (node.system?.cpus || 1)
}

// decimal is Beszel's rule for a meter's number: one decimal from 10, two below.
export function decimal(value: number) {
  return value.toFixed(value >= 10 ? 1 : 2)
}

// gib writes bytes as "12.4 GiB".
export function gib(bytes: number) {
  return `${(bytes / 2 ** 30).toFixed(1)} GiB`
}

// usedOfTotal is a meter's title: "12.4 GiB of 31.3 GiB".
export function usedOfTotal(used: number | undefined, total: number | undefined) {
  if (used === undefined || !total) return undefined

  return `${gib(used)} of ${gib(total)}`
}

// summary counts the machines by state, leaving out a state no machine is in: "3 online · 1 offline".
export function summary(nodes: Node[]) {
  return (Object.keys(stateNames) as MachineState[])
    .map((state) => ({ state, count: nodes.filter((n) => machineState(n) === state).length }))
    .filter((s) => s.count > 0)
    .map((s) => `${s.count} ${stateNames[s.state].toLowerCase()}`)
    .join(" · ")
}

// errorText is the server's message for a refused request (a problem's `message`), or a plain fallback.
export function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "The server refused. Try again."
}
