import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"

import "@/api/client"
import { abortChat, getChat, sendChatMessage } from "@/api/generated/sdk.gen"
import type { ChatMessage } from "@/api/generated/types.gen"
import { Spinner } from "@/components/ui/spinner"
import { PendingApprovals } from "@/features/approvals/approval-card"
import { firstBot } from "@/features/bots/bots-state"
import { useRoutines } from "@/features/routines/use-routines"
import { chatsKey } from "@/features/shell/chats"

import { Composer } from "./composer"
import { Conversation } from "./conversation"
import { useChatStream } from "./use-chat-stream"

// ChatBody is a chat without its header: the conversation, what waits for the owner, and the composer. It fills
// the page of one chat and a pane of the grid alike. A message sent shows at once, before the stream keeps it, as
// t3code's optimisticUserMessages.ts: it waits under a seq of its own until the server answers with its seq, and
// goes when the kept one arrives; a refused send takes it away again.
export function ChatBody({ chatId }: { chatId: string }) {
  const queryClient = useQueryClient()
  const view = useChatStream(chatId)
  const chat = useQuery({
    queryKey: ["chat", chatId],
    queryFn: async () => (await getChat({ path: { chat_id: chatId } })).data,
  })
  const [sending, setSending] = useState<ChatMessage[]>([])
  const bot = chat.data?.bot ?? firstBot
  // The Routines the Bot set up from this chat have their card where they were set up.
  const routines = useRoutines({ chatId })
  const setups = (routines.data ?? [])
    .filter((r) => r.created_by === "bot")
    .map((r) => ({ id: r.id, at: r.created_at }))

  async function send(text: string) {
    const shown: ChatMessage = {
      seq: -Date.now(),
      chat_id: chatId,
      role: "owner",
      text,
      created_at: new Date().toISOString(),
    }
    setSending((list) => [...list, shown])

    try {
      const { data } = await sendChatMessage({ path: { chat_id: chatId }, body: { text } })
      setSending((list) => list.map((m) => (m === shown ? data : m)))
    } catch (err) {
      setSending((list) => list.filter((m) => m !== shown))
      throw err
    }

    void queryClient.invalidateQueries({ queryKey: chatsKey })
  }

  return (
    <>
      {chat.data && !chat.data.coordinator && (
        <output className="block px-5 pb-2 text-sm text-muted-foreground">
          No coordinator runs on the server: messages are kept and nothing answers.
        </output>
      )}
      <Conversation
        view={view}
        local={{ sending }}
        bot={bot}
        setups={setups}
        empty={
          <p className="text-center text-sm text-muted-foreground">Send a message to start.</p>
        }
      />
      <div className="px-3 pb-[calc(env(safe-area-inset-bottom)+1rem)] sm:px-12 sm:pb-[calc(env(safe-area-inset-bottom)+1.25rem)]">
        {/* The stream is down and being opened again: a quiet row, as Omnigent's "Connecting…"
            (web/src/pages/ChatIndicators.tsx, ConnectionIndicator). */}
        {view.reconnecting && (
          <output className="mx-auto mb-2 flex w-full max-w-(--chat-content-max-width) items-center justify-center gap-2 text-sm text-muted-foreground">
            <Spinner aria-hidden="true" />
            Reconnecting…
          </output>
        )}
        {/* What pi asked to do with an account and waits for: answered here, where it was asked. The cards
            scroll in their own box, so that the message box stays on the screen under them. */}
        <div className="mx-auto mb-3 flex max-h-[50dvh] w-full max-w-(--chat-content-max-width) flex-col gap-3 overflow-y-auto empty:hidden">
          <PendingApprovals chatId={chatId} />
        </div>
        <Composer
          working={view.working}
          bot={bot}
          onSend={send}
          onStop={() => void abortChat({ path: { chat_id: chatId } })}
        />
      </div>
    </>
  )
}
