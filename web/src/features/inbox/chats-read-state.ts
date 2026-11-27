import type { InboxChat, InboxChatSource } from "@/api/generated/types.gen"

// What the screen of the chats the secretary reads (issue #1270) says, kept apart from its components so that it is
// tested alone.

// serviceNames are the sources whose chats the triage reads, by their Integration.
const serviceNames: Record<string, string> = {
  zalo: "Zalo",
  whatsapp: "WhatsApp",
  lark: "Lark",
  telegram: "Telegram",
}

// sourceKey is a source's account, <integration>:<label>.
export const sourceKey = (s: Pick<InboxChatSource, "integration" | "label">) =>
  `${s.integration}:${s.label}`

// sourceTitle names a source's tab: its service, and its label when the owner has two accounts of that service.
export function sourceTitle(s: InboxChatSource, all: InboxChatSource[]) {
  const name = serviceNames[s.integration] ?? s.integration
  const twins = all.filter((o) => o.integration === s.integration).length

  return twins > 1 ? `${name} · ${s.label}` : name
}

// kindNames name the kinds of chat.
export const kindNames: Record<InboxChat["kind"], string> = {
  direct: "Direct",
  group: "Group",
  bot: "Bot",
}

// why says why a chat is in its state: its kind's default, or the owner's choice and when.
export function why(chat: InboxChat) {
  if (chat.set_by === "owner") {
    const day = chat.set_at
      ? new Date(chat.set_at).toLocaleDateString(undefined, { day: "numeric", month: "short" })
      : ""

    return day ? `Set by you, ${day}` : "Set by you"
  }

  return chat.kind === "bot" ? "Default for bots" : "Default"
}

// defaultsLine says what a chat the owner never set takes, by kind.
export function defaultsLine(d: { direct: boolean; group: boolean; bot: boolean }) {
  const read = [
    d.direct && "direct chats",
    d.group && "groups",
    d.bot && "bots and official accounts",
  ]
  const skipped = [
    !d.direct && "direct chats",
    !d.group && "groups",
    !d.bot && "bots and official accounts",
  ]
  const list = (xs: (string | false)[]) => xs.filter(Boolean).join(" and ")
  const parts = [
    list(read) && `${list(read)} are read`,
    list(skipped) && `${list(skipped)} are skipped`,
  ]

  return `New chats: ${parts.filter(Boolean).join("; ")}.`
}
