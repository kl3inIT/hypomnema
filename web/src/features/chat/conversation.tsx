import { ArrowDown } from "lucide-react"
import { StickToBottom, useStickToBottomContext } from "use-stick-to-bottom"

import { Button } from "@/components/ui/button"

import { ChatItemView } from "./chat-items"
import { chatItems, type ChatView } from "./chat-state"

// The scrolling conversation. It follows new text while the reader is at the bottom and stops when they scroll up,
// with a button back down, as ai-elements' Conversation (.tmp/omnigent/web/src/components/ai-elements/conversation.tsx).
export function Conversation({ view, empty }: { view: ChatView; empty: React.ReactNode }) {
  const items = chatItems(view)

  return (
    <StickToBottom
      className="relative min-h-0 flex-1 overflow-y-hidden"
      initial="instant"
      resize="smooth"
      role="log"
    >
      <StickToBottom.Content className="mx-auto flex w-full max-w-3xl flex-col gap-4 p-4">
        {items.length === 0
          ? empty
          : items.map((item) => <ChatItemView key={item.key} item={item} />)}
        {view.working && items.at(-1)?.kind === "owner" && (
          <p className="text-sm text-muted-foreground" aria-live="polite">
            pi is working…
          </p>
        )}
      </StickToBottom.Content>
      <ScrollDown />
    </StickToBottom>
  )
}

function ScrollDown() {
  const { isAtBottom, scrollToBottom } = useStickToBottomContext()
  if (isAtBottom) return null

  return (
    <Button
      variant="outline"
      size="icon"
      aria-label="Scroll to the latest message"
      className="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full"
      onClick={() => void scrollToBottom()}
    >
      <ArrowDown />
    </Button>
  )
}
