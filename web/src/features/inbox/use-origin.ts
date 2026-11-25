import { useQueries, useQuery } from "@tanstack/react-query"

import "@/api/client"
import {
  getMeetingOptions,
  listMessagingChatsOptions,
  listMessagingMessagesOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import { useServices } from "@/features/settings/use-integrations"

import { lineOf, meetingOf, sourceOf, type Origin, type Row } from "./inbox-state"

// The Inbox names where an item came from by what the server keeps (issue #1110), as Chatwoot names a conversation by
// its contact and inbox rather than an address (ConversationCard.vue, InboxName.vue): the chats kept of the owner's
// Zalo and WhatsApp accounts, the message an item was read from, the meeting and its line. A chat's name or a sent
// message does not change, so each is read once and kept for the page's life; a chat list is read again every few
// minutes for the new ones. An account whose session is closed answers nothing, and the item keeps its reference's
// own label.
const messagingServices = new Set(["zalo", "whatsapp"])
const chatsFor = 5 * 60_000
const chatsRead = 500

// KeptChats are the owner's kept chats, by "<service>:<chat id>": the chat's name and the account's label it is in.
export type KeptChats = ReadonlyMap<string, { name: string; label: string }>

export function useKeptChats(): KeptChats {
  const services = useServices()
  const accounts = (services.data ?? [])
    .filter((s) => s.kind === "device" && messagingServices.has(s.id))
    .flatMap((s) => (s.connections ?? []).map((c) => ({ integration: s.id, label: c.label })))
  return useQueries({
    queries: accounts.map((path) => ({
      ...listMessagingChatsOptions({ path, query: { limit: chatsRead } }),
      staleTime: chatsFor,
      retry: false,
    })),
    combine: (lists) => {
      const chats = new Map<string, { name: string; label: string }>()
      lists.forEach((list, i) => {
        const { integration, label } = accounts[i]!
        for (const chat of list.data?.data ?? []) {
          if (chat.name) chats.set(`${integration}:${chat.id}`, { name: chat.name, label })
        }
      })

      return chats
    },
  })
}

// MessageAt names one message of a chat the server keeps of the owner's Zalo or WhatsApp account: the service, the
// account's label, the chat and the message's id. A part left out reads nothing.
export type MessageAt = { integration?: string; label?: string; chat?: string; id?: string }

// useMessage reads one kept message, its sender and time; a message does not change, so it is read once.
export function useMessage({ integration, label, chat, id }: MessageAt) {
  return useQuery({
    ...listMessagingMessagesOptions({
      path: { integration: integration ?? "", label: label ?? "" },
      query: { chat, id, limit: 1 },
    }),
    enabled: !!integration && !!label && !!chat && !!id,
    staleTime: Infinity,
    retry: false,
    select: (page) => page.data.find((m) => m.id === id),
  })
}

// useOrigin reads what the server keeps of one row's source: its chat's name among the kept chats, its message's
// sender and time, its meeting's title and who said its line.
export function useOrigin(row: Row, chats: KeptChats): Origin {
  const todo = row.kind === "todo" ? row.todo : undefined
  const source = sourceOf(row)
  const service = todo && messagingServices.has(source) ? source : undefined
  const chatId = service ? todo?.source.id : undefined
  const chat = chatId ? chats.get(`${service}:${chatId}`) : undefined
  const ref = todo?.source.ref
  const messageId = service && ref?.kind === `${service}_message` ? ref.ref : undefined
  // The account a chat was found in, else the one the item names: the Connection's label (issue #1105).
  const label = chat?.label ?? todo?.source.account
  const message = useMessage({ integration: service, label, chat: chatId, id: messageId })

  const meetingId = todo && source === "meeting" ? meetingOf(todo) : undefined
  const meeting = useQuery({
    ...getMeetingOptions({ path: { meeting_id: meetingId ?? "" } }),
    enabled: !!meetingId,
    staleTime: chatsFor,
    retry: false,
  })
  const n = ref?.kind === "meeting_line" ? lineOf(ref.ref) : undefined
  const line = n ? meeting.data?.lines.find((l) => l.n === n) : undefined
  const speaker = line
    ? meeting.data?.meeting.speakers.find((s) => s.track === line.track && s.label === line.speaker)
    : undefined

  const m = message.data

  return {
    chat: chat?.name,
    sender: m?.sender_name || undefined,
    fromMe: m?.from_me,
    at: m?.at,
    meeting: meeting.data?.meeting.title || undefined,
    speaker: speaker?.name || undefined,
  }
}
