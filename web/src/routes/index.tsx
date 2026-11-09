import { createFileRoute } from "@tanstack/react-router"

import { ChatScreen } from "@/features/chat/chat-screen"

export const Route = createFileRoute("/")({
  component: ChatScreen,
})
