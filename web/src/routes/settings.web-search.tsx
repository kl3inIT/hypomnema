import { createFileRoute } from "@tanstack/react-router"

import { WebSearchScreen } from "@/features/models/web-search-screen"
import { SettingsSidebar } from "@/features/settings/settings-shell"

export const Route = createFileRoute("/settings/web-search")({
  component: () => (
    <>
      <SettingsSidebar />
      <WebSearchScreen />
    </>
  ),
})
