// waitingText says in one sentence why a queued Task has no worker yet (issue #660), from the reason the scheduler
// recorded (the Reason codes in internal/controlplane/orchestration/dispatch_types.go), as OpenMausBot says why a
// thread is queued (server/host/queued-reason.ts) and Coder shows a job's place in the queue
// (site/src/modules/provisioners/ProvisionerStatusAlert.tsx). A Task with no reason, or one not ready, says nothing.
// A Bot has no limit of its own since issue #731: it shares the machines' room, so no reason names a Bot.
export function waitingText(reason: string | undefined): string | undefined {
  if (!reason) return undefined

  const [kind, ...rest] = reason.split(":")
  const arg = rest.join(":")

  switch (kind) {
    case "nodes_full":
      return "Waiting: every machine that can run it is busy"
    case "no_node_fits":
      return "Waiting: it asks for more CPU or memory than any machine that can run it has"
    case "nodes_low_memory":
      return "Waiting: every machine that can run it is low on memory"
    case "accounts_cooling":
      return "Waiting: every account of its agent waits for its limit to reset"
    case "no_node":
      return "Waiting: no machine online can run its agent"
    case "no_account":
      return "Waiting: no account can run its agent"
    case "needs_gateway":
      return "Waiting: its agent runs only through the model gateway"
    case "no_node_reaches":
      return `Waiting: no machine reaches ${arg}`
    case "no_node_has_tool":
      return `Waiting: no machine has ${arg} installed`
    default:
      return `Waiting: ${reason}`
  }
}
