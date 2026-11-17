import { createFileRoute } from "@tanstack/react-router"

import { AudioScreen } from "@/features/models/audio-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/audio")({
  component: () => (
    <>
      <SettingsSidebar />
      <AudioScreen />
    </>
  ),
})
