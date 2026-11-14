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
  | "stopping"
  | "stopped"
  | "starting"

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
  // A ready machine that is not on says so: its node is drained and offline because of it, not on its own.
  if (node.machine?.power) return node.machine.power

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
  stopping: "Stopping",
  stopped: "Stopped",
  starting: "Starting",
}

// machineNote is what the page says in place of measurements for a provider's machine that is not ready: what is
// happening and what the owner can do. Undefined for a machine that is ready, or the owner's own.
export function machineNote(node: Node) {
  switch (machineState(node)) {
    case "creating":
      return "It installs the node itself and comes online about five minutes after it was asked for."
    case "create_failed":
      return "Nothing runs for it. Remove it, then add it again."
    case "removing":
      return "Its machine is being destroyed at the provider."
    case "remove_failed":
      return "Its machine may still run and cost money. Destroying it is tried again until it works."
    case "stopping":
      return "Its machine is being turned off."
    case "stopped":
      return "Its machine is off and takes no work. The provider bills for its disk, not for the machine. Everything on it is kept."
    case "starting":
      return "Its machine is being turned on and comes online in about a minute."
    default:
      return undefined
  }
}

// noTerminal is why a machine's terminal cannot be opened now, or undefined when it can: the machine must be
// connected, and its node must have said that it opens one.
export function noTerminal(node: Node) {
  if (!node.connected) return "The machine is not connected."
  if (!node.terminal) {
    return "Its node does not offer a terminal: its configuration turned it off, or its system has none."
  }

  return undefined
}

// machineProblem is why a provider's machine was not created, destroyed, stopped or started, in the provider's
// words.
export function machineProblem(node: Node) {
  const state = machineState(node)

  return ["create_failed", "remove_failed", "stopping", "starting"].includes(state)
    ? node.machine?.error
    : undefined
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

// gpuTitle names a GPU and what it draws: "NVIDIA RTX 4090 · 312 W".
export function gpuTitle(gpu: { name: string; power_watts?: number }) {
  return gpu.power_watts === undefined ? gpu.name : `${gpu.name} · ${Math.round(gpu.power_watts)} W`
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

// The states the View menu filters by, as Beszel's Status group offers Up, Down and Paused
// (systems-table.tsx); a machine in another state, being created or removed, shows under All machines only.
export const filterStates = ["online", "drained", "offline"] as const

export type FilterState = (typeof filterStates)[number]

// statusChoices are the View menu's Status group with each state's count: "Online (3)".
export function statusChoices(nodes: Node[]) {
  return [
    { value: "all", label: "All machines" },
    ...filterStates.map((state) => ({
      value: state,
      label: `${stateNames[state]} (${nodes.filter((n) => machineState(n) === state).length})`,
    })),
  ]
}

// withStatus is the machines the View menu's choice lets through.
export function withStatus(nodes: Node[], status: FilterState | "all") {
  return status === "all" ? nodes : nodes.filter((n) => machineState(n) === status)
}

// errorText is the server's message for a refused request (a problem's `message`), or a plain fallback.
export function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "The server refused. Try again."
}
