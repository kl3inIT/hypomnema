import { useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { abortChat, getChat, sendChatMessage } from "@/api/generated/sdk.gen"
import { PendingApprovals } from "@/features/approvals/approval-card"
import { chatsKey } from "@/features/shell/chats"

import { Composer } from "./composer"
import { Conversation } from "./conversation"
import { useChatStream } from "./use-chat-stream"

// ChatBody is a chat without its header: the conversation, what waits for the owner, and the composer. It fills
// the page of one chat and a pane of the grid alike.
export function ChatBody({ chatId }: { chatId: string }) {
  const queryClient = useQueryClient()
  const view = useChatStream(chatId)
  const chat = useQuery({
    queryKey: ["chat", chatId],
    queryFn: async () => (await getChat({ path: { chat_id: chatId } })).data,
  })

  return (
    <>
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
        {/* What pi asked to do with an account and waits for: answered here, where it was asked. */}
        <div className="mx-auto mb-3 w-full max-w-(--chat-content-max-width) empty:hidden">
          <PendingApprovals chatId={chatId} />
        </div>
        <Composer
          working={view.working}
          onSend={async (text) => {
            await sendChatMessage({ path: { chat_id: chatId }, body: { text } })
            void queryClient.invalidateQueries({ queryKey: chatsKey })
          }}
          onStop={() => void abortChat({ path: { chat_id: chatId } })}
        />
      </div>
    </>
  )
}
