import { createFileRoute } from "@tanstack/react-router"

import { BotsScreen } from "@/features/bots/bots-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/bots/")({
  component: () => (
    <>
      <AppSidebar />
      <BotsScreen />
    </>
  ),
})
