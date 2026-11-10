import { createFileRoute } from "@tanstack/react-router"

import { IntegrationsScreen } from "@/features/settings/integrations-screen"
import { SettingsSidebar } from "@/features/settings/settings-shell"

export const Route = createFileRoute("/settings/integrations")({
  component: () => (
    <>
      <SettingsSidebar />
      <IntegrationsScreen />
    </>
  ),
})
