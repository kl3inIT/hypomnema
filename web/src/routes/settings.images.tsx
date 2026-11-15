import { createFileRoute } from "@tanstack/react-router"

import { ImagesScreen } from "@/features/models/images-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/images")({
  component: () => (
    <>
      <SettingsSidebar />
      <ImagesScreen />
    </>
  ),
})
