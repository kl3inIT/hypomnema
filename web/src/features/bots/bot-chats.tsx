import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { MessagesSquare } from "lucide-react"

import "@/api/client"
import { listChatsOptions } from "@/api/generated/@tanstack/react-query.gen"
import type { Bot } from "@/api/generated/types.gen"
import { usePending } from "@/features/approvals/use-approvals"
import { ChatMenu } from "@/features/shell/chat-menu"
import { since } from "@/lib/time"

// BotChats lists a Bot's chats other than its main chat on its page (issue #471): the sidebar shows the Bot as one
// row that opens its main chat, and every other chat (a former main chat, a Lark chat, a Routine's run) keeps its id,
// its work and its address and opens from here, as OpenMausBot keeps a bot's threads reachable when the sidebar hides
// them (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; src/lib/thread-preferences.ts). A plain
// list, newest first, each with its menu to delete it; a chat that waits on the owner says so.
export function BotChats({ bot }: { bot: Bot }) {
  const chats = useQuery({
    ...listChatsOptions({ query: { bot: bot.id, limit: 500 } }),
    select: (page) => page.data,
  })
  const pending = usePending()
  const waiting = new Set(pending.data?.flatMap((a) => (a.chat_id ? [a.chat_id] : [])))
  const others = (chats.data ?? []).filter((c) => c.id !== bot.main_chat)

  return (
    <section aria-labelledby="bot-chats" className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <MessagesSquare className="size-4 text-primary" aria-hidden />
        <h3 id="bot-chats" className="min-w-0 flex-1 text-sm font-medium">
          Other chats
        </h3>
        <span className="text-xs text-muted-foreground tabular-nums">{others.length}</span>
      </div>
      {bot.main_chat && (
        <Link
          to="/chats/$chatId"
          params={{ chatId: bot.main_chat }}
          className="self-start text-xs text-link hover:underline"
        >
          Open the main chat
        </Link>
      )}
      {chats.error ? (
        <p role="alert" className="rounded-lg bg-failed-soft p-3 text-xs text-failed">
          The chats could not be read.
        </p>
      ) : chats.isPending ? (
        <p className="text-xs text-muted-foreground">Loading…</p>
      ) : others.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No other chats: this Bot talks in its main chat.
        </p>
      ) : (
        <ul aria-label={`Other chats of ${bot.name}`} className="flex flex-col gap-px">
          {others.map((c) => (
            <li key={c.id} className="group/chat relative">
              <Link
                to="/chats/$chatId"
                params={{ chatId: c.id }}
                className="flex min-h-9 items-center gap-2.5 rounded-md px-2.5 py-1.5 pe-10 text-sm outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
              >
                <span className="min-w-0 flex-1 truncate">{c.title}</span>
                <span className="shrink-0 text-xs text-secondary-label tabular-nums">
                  {waiting.has(c.id) ? (
                    <span className="font-medium text-needs-you">Needs you</span>
                  ) : c.working ? (
                    "Working"
                  ) : (
                    since(c.updated_at)
                  )}
                </span>
              </Link>
              <ChatMenu
                chat={c}
                current={false}
                className="absolute end-1.5 top-1/2 -translate-y-1/2"
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
