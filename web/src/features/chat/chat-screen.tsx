import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { MessageSquarePlus } from "lucide-react"

import "@/api/client"
import { abortChat, createChat, getChat, listChats, sendChatMessage } from "@/api/generated/sdk.gen"
import type { Chat } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"

import { Composer } from "./composer"
import { Conversation } from "./conversation"
import { useChatStream } from "./use-chat-stream"

// The chat with pi, the coordinator on the server: the list of chats beside the open one. Without a chat open, the
// first message starts one.
export function ChatScreen({ chatId }: { chatId?: string }) {
  return (
    <div className="flex min-h-0 flex-1">
      <ChatList current={chatId} />
      {chatId ? <OpenChat key={chatId} chatId={chatId} /> : <NewChat />}
    </div>
  )
}

const chatsKey = ["chats"] as const

function useChats() {
  return useQuery({ queryKey: chatsKey, queryFn: async () => (await listChats()).data.data })
}

// ChatList is a column on wide screens; on a phone the header's Chats link opens the start screen, which lists them.
function ChatList({ current, inline }: { current?: string; inline?: boolean }) {
  const chats = useChats()

  return (
    <nav
      aria-label="Chats"
      className={
        inline
          ? "flex flex-col gap-1"
          : "hidden w-64 shrink-0 flex-col gap-1 overflow-y-auto border-r p-2 md:flex"
      }
    >
      {!inline && (
        <Button
          variant="ghost"
          className="justify-start"
          render={<Link to="/" />}
          nativeButton={false}
        >
          <MessageSquarePlus />
          New chat
        </Button>
      )}
      {chats.data?.map((c) => (
        <ChatLink key={c.id} chat={c} current={c.id === current} />
      ))}
    </nav>
  )
}

function ChatLink({ chat, current }: { chat: Chat; current: boolean }) {
  return (
    <Link
      to="/chats/$chatId"
      params={{ chatId: chat.id }}
      aria-current={current ? "page" : undefined}
      className="flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm hover:bg-muted aria-[current=page]:bg-muted"
    >
      <span className="truncate">{chat.title}</span>
      {chat.working && (
        <span className="ml-auto size-2 shrink-0 rounded-full bg-running" aria-label="working" />
      )}
    </Link>
  )
}

function NewChat() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const start = useMutation({
    mutationFn: async (text: string) => {
      const { data: chat } = await createChat({
        body: { title: text.split("\n")[0]?.slice(0, 80) },
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
    <div className="flex min-w-0 flex-1 flex-col">
      <section className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-4 overflow-y-auto p-6">
        <h1 className="text-lg font-medium">What should be built?</h1>
        <p className="text-muted-foreground">
          pi, the coordinator, plans the work, gives it to workers on your machines, and reports
          back here.
        </p>
        <div className="md:hidden">
          <ChatList inline />
        </div>
      </section>
      <Composer onSend={(text) => start.mutateAsync(text)} onStop={() => {}} working={false} />
    </div>
  )
}

function OpenChat({ chatId }: { chatId: string }) {
  const queryClient = useQueryClient()
  const view = useChatStream(chatId)
  const chat = useQuery({
    queryKey: ["chat", chatId],
    queryFn: async () => (await getChat({ path: { chat_id: chatId } })).data,
  })

  return (
    <div className="flex min-w-0 flex-1 flex-col">
      {chat.data && !chat.data.coordinator && (
        <output className="block border-b px-4 py-2 text-sm text-muted-foreground">
          No coordinator runs on the server: messages are kept and nothing answers.
        </output>
      )}
      <Conversation view={view} empty={<p className="text-muted-foreground">No messages yet.</p>} />
      <Composer
        working={view.working}
        onSend={async (text) => {
          await sendChatMessage({ path: { chat_id: chatId }, body: { text } })
          void queryClient.invalidateQueries({ queryKey: chatsKey })
        }}
        onStop={() => void abortChat({ path: { chat_id: chatId } })}
      />
    </div>
  )
}
