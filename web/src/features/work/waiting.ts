import type { Bot } from "@/api/generated/types.gen"

// waitingText says in one sentence why a queued Task has no worker yet (issue #660), from the reason the scheduler
// recorded (the Reason codes in internal/controlplane/orchestration/dispatch_types.go), as OpenMausBot says why a
// thread is queued (server/host/queued-reason.ts) and Coder shows a job's place in the queue
// (site/src/modules/provisioners/ProvisionerStatusAlert.tsx). bots names a Bot by its id; one it does not know is
// named by its id. A Task with no reason, or one not ready, says nothing.
export function waitingText(reason: string | undefined, bots: Bot[] = []): string | undefined {
  if (!reason) return undefined

  const [kind, ...rest] = reason.split(":")
  const arg = rest.join(":")

  switch (kind) {
    case "bot_at_limit": {
      if (!arg) return "Waiting: your Runs without a Bot already run as many workers as they may"
      const bot = bots.find((b) => b.id === arg)
      if (!bot) return `Waiting: its Bot already runs as many workers as it may`
      return `Waiting: ${bot.name} is at its limit of ${bot.max_dispatches} workers at once`
    }
    case "nodes_full":
      return "Waiting: every machine that can run it is busy"
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
