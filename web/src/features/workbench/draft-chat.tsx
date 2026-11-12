import type { Chat } from "@/api/generated/types.gen"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { firstBot } from "@/features/bots/bots-state"
import { Composer } from "@/features/chat/composer"
import { useStartChat } from "@/features/chat/start-chat"

// DraftChat is a new chat in a pane's tab (issue #235): the composer of the New chat page, with the Bot picked
// from "+". Its first message makes the chat, which then takes this tab's place.
export function DraftChat({ bot, onStarted }: { bot: string; onStarted: (chat: string) => void }) {
  const start = useStartChat(bot || undefined, (chat: Chat) => onStarted(chat.id))

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3">
      <div className="mx-auto flex w-full flex-1 flex-col items-center justify-center gap-5 py-6">
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <AgentAvatar name={bot || firstBot} className="size-5" />
          A new chat with {bot || firstBot}
        </p>
        <Composer
          bot={bot || firstBot}
          onSend={(text) => start.mutateAsync(text)}
          onStop={() => {}}
          working={false}
        />
      </div>
    </div>
  )
}
