import { createFileRoute } from "@tanstack/react-router"

import { RepositoriesScreen } from "@/features/settings/repositories-screen"
import { SettingsSidebar } from "@/features/settings/settings-shell"

export const Route = createFileRoute("/settings/repositories")({
  component: () => (
    <>
      <SettingsSidebar />
      <RepositoriesScreen />
    </>
  ),
})
