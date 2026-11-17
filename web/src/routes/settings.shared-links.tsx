import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { SharedLinksScreen } from "@/features/artifacts/shared-links-screen"

export const Route = createFileRoute("/settings/shared-links")({
  component: () => (
    <>
      <SettingsSidebar />
      <SharedLinksScreen />
    </>
  ),
})
