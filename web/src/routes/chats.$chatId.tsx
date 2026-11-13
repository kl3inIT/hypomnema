import { createFileRoute } from "@tanstack/react-router"
import { useEffect } from "react"

import { ChatScreen } from "@/features/chat/chat-screen"
import { rememberPlace } from "@/features/shell/last-place"

export const Route = createFileRoute("/chats/$chatId")({
  component: OpenChatRoute,
})

function OpenChatRoute() {
  const { chatId } = Route.useParams()
  useEffect(() => rememberPlace({ kind: "chat", chat: chatId }), [chatId])

  return <ChatScreen chatId={chatId} />
}
