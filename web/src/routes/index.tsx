import { createFileRoute, redirect } from "@tanstack/react-router"

import { ChatScreen } from "@/features/chat/chat-screen"
import { placeToResume } from "@/features/shell/last-place"

type Search = { bot?: string }

// The start page; ?bot= picks the Bot a new chat talks with, as the Team page opens one for a Bot with no chat.
// The app opened here goes back to where the owner last was (last-place.ts).
export const Route = createFileRoute("/")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    bot: typeof s.bot === "string" && s.bot ? s.bot : undefined,
  }),
  beforeLoad: async ({ search }) => {
    if (search.bot) return
    const place = await placeToResume()
    if (place?.kind === "chat") {
      throw redirect({ to: "/chats/$chatId", params: { chatId: place.chat }, replace: true })
    }
    if (place?.kind === "grid") throw redirect({ to: "/grid", replace: true })
  },
  component: StartRoute,
})

function StartRoute() {
  const { bot } = Route.useSearch()

  return <ChatScreen key={bot} bot={bot} />
}
