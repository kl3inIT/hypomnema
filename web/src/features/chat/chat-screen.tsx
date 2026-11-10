import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"

import "@/api/client"
import { abortChat, createChat, getChat, sendChatMessage } from "@/api/generated/sdk.gen"
import {
  AppSidebar,
  ChatList,
  PageHeader,
  Places,
  SettingsLink,
  SignOut,
} from "@/features/shell/app-shell"
import { chatsKey } from "@/features/shell/chats"

import { Composer } from "./composer"
import { Conversation } from "./conversation"
import { useChatStream } from "./use-chat-stream"

// The chat with pi, laid out as t3code's app (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): the app's sidebar (features/shell), and beside it a column with a quiet
// header (ChatView.tsx, ChatHeader.tsx), the conversation and the composer. On a phone the sidebar hides and the
// header's menu opens the start screen, which lists the places and the chats. Without a chat open, the start
// screen's composer starts one (chat/DraftHeroHeadline.tsx).
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
      <PageHeader />
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 sm:px-12">
        <div className="mx-auto flex w-full max-w-5xl flex-1 flex-col items-center justify-center gap-6 py-8">
          <h1 className="w-full text-center text-2xl font-normal tracking-tight text-foreground sm:text-3xl">
            What should be built?
          </h1>
          <Composer onSend={(text) => start.mutateAsync(text)} onStop={() => {}} working={false} />
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
  const queryClient = useQueryClient()
  const view = useChatStream(chatId)
  const chat = useQuery({
    queryKey: ["chat", chatId],
    queryFn: async () => (await getChat({ path: { chat_id: chatId } })).data,
  })

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={chat.data?.title} />
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
