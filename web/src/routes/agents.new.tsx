import { createFileRoute } from "@tanstack/react-router"

import { NewAgentScreen } from "@/features/agents/profile-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/agents/new")({
  component: () => (
    <>
      <AppSidebar />
      <NewAgentScreen />
    </>
  ),
})
