import { createFileRoute } from "@tanstack/react-router"

import { MuseScreen } from "@/features/muse/muse-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/muse")({
  component: () => (
    <>
      <SettingsSidebar />
      <MuseScreen />
    </>
  ),
})
