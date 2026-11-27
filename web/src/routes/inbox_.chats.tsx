import { createFileRoute } from "@tanstack/react-router"

import { ChatsReadScreen } from "@/features/inbox/chats-read-screen"
import { AppSidebar } from "@/features/shell/app-shell"

// Which chats the secretary reads, opened from the gear on the Inbox page (issue #1270).
export const Route = createFileRoute("/inbox_/chats")({
  component: () => (
    <>
      <AppSidebar />
      <ChatsReadScreen />
    </>
  ),
})
