import { createFileRoute } from "@tanstack/react-router"

import { ModelsScreen } from "@/features/models/models-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/models")({
  component: () => (
    <>
      <SettingsSidebar />
      <ModelsScreen />
    </>
  ),
})
