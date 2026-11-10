import { createFileRoute } from "@tanstack/react-router"

import { AgentsScreen } from "@/features/agents/agents-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/agents/")({
  component: () => (
    <>
      <AppSidebar />
      <AgentsScreen />
    </>
  ),
})
