import { Link } from "@tanstack/react-router"

import { useBots } from "@/features/bots/use-bots"
import { useChat } from "@/features/shell/chats"

// BackToMainChat leads from another chat of a Bot back to its main chat (issue #923), as OpenMausBot's chat head
// has "Back to your chat" while the bot shows a thread other than its main one (src/components/ChatView.tsx,
// awayFromMainThread in src/state/thread-navigation.ts; Apache License 2.0): a quiet pill, none on the main chat
// itself or when the Bot has no main chat.
export function BackToMainChat({ chatId }: { chatId: string }) {
  const chat = useChat(chatId).data
  const main = useBots().data?.find((b) => b.id === chat?.bot)?.main_chat
  if (!main || main === chatId) return null

  return (
    <Link
      to="/chats/$chatId"
      params={{ chatId: main }}
      className="shrink-0 rounded-full bg-muted px-2.5 py-0.5 text-xs font-medium whitespace-nowrap text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
    >
      Back to main chat
    </Link>
  )
}
