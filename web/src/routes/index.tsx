import { createFileRoute } from "@tanstack/react-router"

import { ChatScreen } from "@/features/chat/chat-screen"

type Search = { bot?: string }

// The start page; ?bot= picks the Bot a new chat talks with, as the Team page opens one for a Bot with no chat.
export const Route = createFileRoute("/")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    bot: typeof s.bot === "string" && s.bot ? s.bot : undefined,
  }),
  component: StartRoute,
})

function StartRoute() {
  const { bot } = Route.useSearch()

  return <ChatScreen key={bot} bot={bot} />
}
