import { useState } from "react"

import type { Chat } from "@/api/generated/types.gen"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { firstBot } from "@/features/bots/bots-state"
import { AttachDrop } from "@/features/chat/attach-drop"
import { useAttachments } from "@/features/chat/attachments"
import { ChatModelControls } from "@/features/chat/chat-model-picker"
import { useDraftChoice } from "@/features/chat/chat-model-state"
import { newChatId } from "@/features/chat/chat-id"
import { Composer } from "@/features/chat/composer"
import { useStartChat } from "@/features/chat/start-chat"

// DraftChat is a new chat in a pane's tab (issue #235): the composer of the New chat page, with the Bot picked
// from "+". Its first message makes the chat, which then takes this tab's place. As the New chat page, it chooses
// its chat's id when it opens and takes files before the chat is made (issue #357).
export function DraftChat({ bot, onStarted }: { bot: string; onStarted: (chat: string) => void }) {
  const [chatId] = useState(newChatId)
  const attachments = useAttachments(chatId, bot || firstBot)
  const [model, setModel] = useDraftChoice(bot || firstBot)
  const start = useStartChat(chatId, bot || undefined, model, (chat: Chat) => onStarted(chat.id))

  return (
    <AttachDrop
      attachments={attachments}
      className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3"
    >
      <div className="mx-auto flex w-full flex-1 flex-col items-center justify-center gap-5 py-6">
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <AgentAvatar name={bot || firstBot} className="size-5" />
          A new chat with {bot || firstBot}
        </p>
        <Composer
          bot={bot || firstBot}
          onSend={(text, files) => start.mutateAsync({ text, files })}
          onStop={() => {}}
          working={false}
          attachments={attachments}
          controls={<ChatModelControls bot={bot || firstBot} choice={model} onChange={setModel} />}
        />
      </div>
    </AttachDrop>
  )
}
