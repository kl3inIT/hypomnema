import { createFileRoute } from "@tanstack/react-router"

import { NewBotScreen } from "@/features/bots/bot-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/bots/new")({
  component: () => (
    <>
      <AppSidebar />
      <NewBotScreen />
    </>
  ),
})
