import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { CircleDashed, LogOut, Menu, SquarePen } from "lucide-react"

import "@/api/client"
import { abortChat, createChat, getChat, listChats, sendChatMessage } from "@/api/generated/sdk.gen"
import type { Chat } from "@/api/generated/types.gen"
import { saveToken } from "@/features/auth/token"
import { cn } from "@/lib/utils"

import { Composer } from "./composer"
import { Conversation } from "./conversation"
import { useChatStream } from "./use-chat-stream"

// The chat with pi, laid out as t3code's app (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): a full-height sidebar of chats (components/ui/sidebar.tsx,
// sidebar/SidebarThreadRow.tsx), and beside it a column with a quiet 52px header (ChatView.tsx, ChatHeader.tsx),
// the conversation and the composer. On a phone the sidebar hides and the header's menu opens the start screen,
// which lists the chats. Without a chat open, the start screen's composer starts one (chat/DraftHeroHeadline.tsx).
export function ChatScreen({ chatId }: { chatId?: string }) {
  return (
    <>
      <Sidebar current={chatId} />
      {chatId ? <OpenChat key={chatId} chatId={chatId} /> : <NewChat />}
    </>
  )
}

const chatsKey = ["chats"] as const

function useChats() {
  return useQuery({ queryKey: chatsKey, queryFn: async () => (await listChats()).data.data })
}

function Sidebar({ current }: { current?: string }) {
  return (
    <aside className="hidden h-full w-64 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground md:flex">
      <div className="flex h-(--workspace-topbar-height) shrink-0 items-center px-3.5">
        <Link to="/" className="text-sm font-semibold tracking-tight">
          Hypomnema
        </Link>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-2 px-2">
        <Link
          to="/"
          className="flex h-8 items-center gap-2 rounded-lg px-2.5 text-sm font-medium text-muted-foreground/80 hover:bg-sidebar-row-hover hover:text-foreground"
        >
          <SquarePen className="size-4" />
          New chat
        </Link>
        <ChatList current={current} />
      </div>
      <div className="p-2">
        <SignOut />
      </div>
    </aside>
  )
}

function SignOut() {
  return (
    <button
      type="button"
      onClick={() => saveToken(null)}
      className="flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-sm font-medium text-muted-foreground/80 hover:bg-sidebar-row-hover hover:text-foreground"
    >
      <LogOut className="size-4" />
      Sign out
    </button>
  )
}

function ChatList({ current }: { current?: string }) {
  const chats = useChats()

  if (chats.data?.length === 0) {
    return <p className="px-2 py-6 text-center text-xs text-muted-foreground">No chats yet.</p>
  }

  return (
    <nav aria-label="Chats" className="-mx-2 min-h-0 flex-1 overflow-y-auto px-2">
      <p className="px-2.5 pt-2 pb-1 text-xs font-medium text-muted-foreground">Chats</p>
      <div className="flex flex-col gap-px">
        {chats.data?.map((c) => (
          <ChatRow key={c.id} chat={c} current={c.id === current} />
        ))}
      </div>
    </nav>
  )
}

const ago = new Intl.RelativeTimeFormat(undefined, { style: "narrow" })

function since(iso: string) {
  const minutes = Math.round((Date.now() - new Date(iso).getTime()) / 60_000)
  if (minutes < 60) return ago.format(-minutes, "minute")
  if (minutes < 60 * 24) return ago.format(-Math.round(minutes / 60), "hour")

  return ago.format(-Math.round(minutes / (60 * 24)), "day")
}

function ChatRow({ chat, current }: { chat: Chat; current: boolean }) {
  return (
    <Link
      to="/chats/$chatId"
      params={{ chatId: chat.id }}
      aria-current={current ? "page" : undefined}
      className={cn(
        "group/sidebar-row relative flex h-9 w-full items-center gap-2.5 overflow-hidden rounded-md px-2.5 text-left outline-none select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
        current
          ? "bg-sidebar-row-active [:root:not(.dark)_&]:ring-1 [:root:not(.dark)_&]:ring-sidebar-border/70 [:root:not(.dark)_&]:ring-inset"
          : "hover:bg-sidebar-row-hover",
      )}
    >
      <span
        className={cn(
          "min-w-0 flex-1 truncate text-sm",
          current || chat.working ? "font-medium text-foreground" : "text-foreground/75",
        )}
      >
        {chat.title}
      </span>
      <span className="ml-auto flex h-6 min-w-8 shrink-0 items-center justify-end text-xs text-secondary-label tabular-nums">
        {chat.working ? (
          <span className="inline-flex items-center gap-1 font-medium text-info-foreground">
            <CircleDashed className="size-3.5 animate-spin [animation-duration:3s]" aria-hidden />
            Working
          </span>
        ) : (
          since(chat.updated_at)
        )}
      </span>
    </Link>
  )
}

// Header is the quiet bar above the conversation: the chat's title, and on a phone a way back to the chats.
function Header({ title }: { title?: string }) {
  return (
    <header className="flex h-(--workspace-topbar-height) shrink-0 items-center gap-2 bg-background px-3 sm:px-5">
      <Link
        to="/"
        aria-label="Chats"
        className="-ms-1.5 flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground md:hidden"
      >
        <Menu className="size-4" />
      </Link>
      {title && <h1 className="min-w-0 truncate text-sm font-medium text-foreground">{title}</h1>}
    </header>
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
    <main className="flex min-w-0 flex-1 flex-col">
      <Header />
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 sm:px-12">
        <div className="mx-auto flex w-full max-w-5xl flex-1 flex-col items-center justify-center gap-6 py-8">
          <h1 className="w-full text-center text-2xl font-normal tracking-tight text-foreground sm:text-3xl">
            What should be built?
          </h1>
          <Composer onSend={(text) => start.mutateAsync(text)} onStop={() => {}} working={false} />
          <p className="max-w-(--chat-content-max-width) text-center text-sm text-muted-foreground">
            pi plans the work, gives it to workers on your machines, and reports back here.
          </p>
        </div>
        <div className="mx-auto w-full max-w-(--chat-content-max-width) pb-6 md:hidden">
          <ChatList />
          <div className="mt-4">
            <SignOut />
          </div>
        </div>
      </div>
    </main>
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
    <main className="flex min-w-0 flex-1 flex-col">
      <Header title={chat.data?.title} />
      {chat.data && !chat.data.coordinator && (
        <output className="block px-5 pb-2 text-sm text-muted-foreground">
          No coordinator runs on the server: messages are kept and nothing answers.
        </output>
      )}
      <Conversation
        view={view}
        empty={
          <p className="text-center text-sm text-muted-foreground">Send a message to start.</p>
        }
      />
      <div className="px-3 pb-[calc(env(safe-area-inset-bottom)+1rem)] sm:px-12 sm:pb-[calc(env(safe-area-inset-bottom)+1.25rem)]">
        <Composer
          working={view.working}
          onSend={async (text) => {
            await sendChatMessage({ path: { chat_id: chatId }, body: { text } })
            void queryClient.invalidateQueries({ queryKey: chatsKey })
          }}
          onStop={() => void abortChat({ path: { chat_id: chatId } })}
        />
      </div>
    </main>
  )
}
