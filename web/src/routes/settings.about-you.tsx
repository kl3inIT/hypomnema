import { createFileRoute } from "@tanstack/react-router"

import { AboutYouScreen } from "@/features/memory/about-you-screen"
import { SettingsSidebar } from "@/features/settings/settings-shell"

export const Route = createFileRoute("/settings/about-you")({
  component: () => (
    <>
      <SettingsSidebar />
      <AboutYouScreen />
    </>
  ),
})
