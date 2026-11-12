import type { LarkChannel, LarkChannelMode } from "@/api/generated/types.gen"

// What the Channels page and a Bot's page say about a Lark Channel, apart from how they draw it.

// A Channel's name follows the server's rule (orchestration/larkchannels.go, ValidLarkChannelName): lowercase
// letters, digits and dashes, starting with a letter, at most 50.
export const channelNamePattern = /^[a-z][a-z0-9-]{0,49}$/

export const modeNames: Record<LarkChannelMode, string> = {
  owner: "Only you",
  allowlist: "You and the people you add",
  everyone: "Everyone in your Lark organization",
}

// channelState is the one word a Channel's badge says, and whether it is the good one.
export function channelState(channel: Pick<LarkChannel, "enabled" | "connected">): {
  label: string
  good: boolean
} {
  if (!channel.enabled) return { label: "Off", good: false }
  if (channel.connected) return { label: "Connected", good: true }

  return { label: "Not connected", good: false }
}

// nameProblem says what keeps a name from being a new Channel's, in the form's own words. PUT changes a Channel
// of the same name, so a name that is taken is refused here.
export function nameProblem(typed: string, taken: string[]): string | null {
  const name = typed.trim()

  if (!name) return "Give the channel a name."
  if (!channelNamePattern.test(name)) {
    return "Use lowercase letters, digits and dashes, starting with a letter; at most 50."
  }
  if (taken.includes(name)) return "A channel has this name already."

  return null
}

// channelsOf are the Channels that talk to a Bot, in the order the server lists them.
export function channelsOf<C extends Pick<LarkChannel, "bot">>(bot: string, channels: C[]): C[] {
  return channels.filter((channel) => channel.bot === bot)
}

// botChoices are the Bots a Channel can talk to: every Bot, and the Channel's own while the Bots are still
// being read, so that its select never shows another.
export function botChoices(bots: string[], current?: string): string[] {
  return current && !bots.includes(current) ? [current, ...bots] : bots
}
