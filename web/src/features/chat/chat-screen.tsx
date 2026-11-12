import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { useState } from "react"

import "@/api/client"
import { abortChat, createChat, getChat, sendChatMessage } from "@/api/generated/sdk.gen"
import type { ChatMessage } from "@/api/generated/types.gen"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { firstBot } from "@/features/bots/bots-state"
import { useBots } from "@/features/bots/use-bots"
import {
  AppSidebar,
  ChatList,
  PageHeader,
  Places,
  SettingsLink,
  SignOut,
} from "@/features/shell/app-shell"
import { chatsKey } from "@/features/shell/chats"
import { GridDrop } from "@/features/workbench/grid-drop"
import { splitGrid } from "@/features/workbench/grid-layout"
import { setGrid, useGridOffered } from "@/features/workbench/use-grid"

import { PendingApprovals } from "@/features/approvals/approval-card"

import { Composer } from "./composer"
import { Conversation } from "./conversation"
import { useChatStream } from "./use-chat-stream"

// The chat with pi, laid out as t3code's app (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): the app's sidebar (features/shell), and beside it a column with a quiet
// header (ChatView.tsx, ChatHeader.tsx), the conversation and the composer. On a phone the sidebar hides and the
// header's menu opens the start screen, which lists the places and the chats. Without a chat open, the start
// screen's composer starts one (chat/DraftHeroHeadline.tsx). A chat talks with one Bot (issue #169): the start
// screen offers the Bots when there are several, and an open chat's header names its Bot.
export function ChatScreen({ chatId }: { chatId?: string }) {
  return (
    <>
      <AppSidebar currentChat={chatId} />
      {chatId ? <OpenChat key={chatId} chatId={chatId} /> : <NewChat />}
    </>
  )
}

function NewChat() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  // The Bot the new chat talks with. With one Bot there is nothing to choose and the server takes the first.
  const bots = useBots().data ?? []
  const [picked, setPicked] = useState(firstBot)
  const choice = bots.length > 1
  const bot = bots.some((b) => b.name === picked) ? picked : firstBot

  const start = useMutation({
    mutationFn: async (text: string) => {
      const { data: chat } = await createChat({
        body: { title: text.split("\n")[0]?.slice(0, 80), ...(choice && { bot }) },
      })
      await sendChatMessage({ path: { chat_id: chat.id }, body: { text } })

      return chat
    },
    onSuccess: async (chat) => {
      await queryClient.invalidateQueries({ queryKey: chatsKey })
      await navigate({ to: "/chats/$chatId", params: { chatId: chat.id } })
    },
  })

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 sm:px-12">
        <div className="mx-auto flex w-full max-w-5xl flex-1 flex-col items-center justify-center gap-6 py-8">
          <h1 className="w-full text-center text-2xl font-normal tracking-tight text-foreground sm:text-3xl">
            What should be built?
          </h1>
          {choice && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              Chat with
              <Select value={bot} onValueChange={(value) => value && setPicked(value)}>
                <SelectTrigger aria-label="Bot" size="sm">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {bots.map((b) => (
                    <SelectItem key={b.name} value={b.name}>
                      {b.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <Composer
            onSend={(text) => start.mutateAsync(text)}
            onStop={() => {}}
            working={false}
            bot={bot}
          />
          <p className="max-w-(--chat-content-max-width) text-center text-sm text-muted-foreground">
            pi plans the work, gives it to workers on your machines, and reports back here.
          </p>
          <div className="w-full max-w-(--chat-content-max-width) md:hidden">
            <Places />
            <ChatList label="Recent chats" />
            <div className="mt-2 flex flex-col gap-px">
              <SettingsLink />
              <SignOut />
            </div>
          </div>
        </div>
      </div>
    </main>
  )
}

function OpenChat({ chatId }: { chatId: string }) {
  const navigate = useNavigate()
  const offered = useGridOffered()
  const chat = useQuery({
    queryKey: ["chat", chatId],
    queryFn: async () => (await getChat({ path: { chat_id: chatId } })).data,
  })

  return (
    <main className="relative flex min-w-0 flex-1 flex-col">
      <PageHeader title={chat.data?.title}>
        {chat.data && (
          <Link
            to="/bots/$name"
            params={{ name: chat.data.bot }}
            aria-label={`Bot ${chat.data.bot}`}
            className="ms-auto flex shrink-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <AgentAvatar name={chat.data.bot} className="size-5" />
            {chat.data.bot}
          </Link>
        )}
      </PageHeader>
      <ChatBody chatId={chatId} />
      {/* Another chat dragged here from the sidebar makes a grid of the two (issue #235). */}
      {offered && (
        <GridDrop
          chat={chatId}
          splits={(dragged, edge) => splitGrid(chatId, chatId, dragged, edge) !== null}
          onSplit={(dragged, edge) => {
            setGrid(splitGrid(chatId, chatId, dragged, edge))
            void navigate({ to: "/grid" })
          }}
        />
      )}
    </main>
  )
}

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
  const [stoppedAfter, setStoppedAfter] = useState<number>()
  const bot = chat.data?.bot ?? firstBot

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
        local={{ sending, stoppedAfter }}
        bot={bot}
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
        {/* What pi asked to do with an account and waits for: answered here, where it was asked. */}
        <div className="mx-auto mb-3 w-full max-w-(--chat-content-max-width) empty:hidden">
          <PendingApprovals chatId={chatId} />
        </div>
        <Composer
          working={view.working}
          bot={bot}
          onSend={send}
          onStop={() => {
            setStoppedAfter(Math.max(0, ...view.messages.map((m) => m.seq)))
            void abortChat({ path: { chat_id: chatId } })
          }}
        />
      </div>
    </>
  )
}
