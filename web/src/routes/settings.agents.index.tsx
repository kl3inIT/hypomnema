import { createFileRoute } from "@tanstack/react-router"

import { AgentsScreen } from "@/features/agents/agents-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/agents/")({
  component: () => (
    <>
      <SettingsSidebar />
      <AgentsScreen />
    </>
  ),
})
