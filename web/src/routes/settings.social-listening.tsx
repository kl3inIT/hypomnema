import { createFileRoute } from "@tanstack/react-router"

import { ListeningScreen } from "@/features/listening/listening-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/social-listening")({
  component: () => (
    <>
      <SettingsSidebar />
      <ListeningScreen />
    </>
  ),
})
