import { useBots } from "@/features/bots/use-bots"
import { useChat } from "@/features/shell/chats"

// BackToMainChat leads from another chat of a Bot back to its main chat (issue #923), as OpenMausBot's chat head
// has "Back to your chat" while the bot shows a thread other than its main one (src/components/ChatView.tsx,
// awayFromMainThread in src/state/thread-navigation.ts; Apache License 2.0): a quiet pill at the end of a pane's
// strip, for the chat in front of the pane, none on the main chat itself, on a tab that is not a chat, or when the
// Bot has no main chat. Where the main chat opens is the pane's to say (onBack).
export function BackToMainChat({
  chatId,
  onBack,
}: {
  chatId: string | null
  onBack: (main: string) => void
}) {
  const chat = useChat(chatId).data
  const main = useBots().data?.find((b) => b.id === chat?.bot)?.main_chat
  if (!chatId || !main || main === chatId) return null

  return (
    <button
      type="button"
      onClick={() => onBack(main)}
      className="shrink-0 rounded-full bg-muted px-2.5 py-0.5 text-xs font-medium whitespace-nowrap text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
    >
      Back to main chat
    </button>
  )
}
