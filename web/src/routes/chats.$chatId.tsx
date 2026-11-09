import { createFileRoute } from "@tanstack/react-router"

import { ChatScreen } from "@/features/chat/chat-screen"

export const Route = createFileRoute("/chats/$chatId")({
  component: OpenChatRoute,
})

function OpenChatRoute() {
  const { chatId } = Route.useParams()

  return <ChatScreen chatId={chatId} />
}
