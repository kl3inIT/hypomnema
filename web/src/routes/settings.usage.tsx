import { createFileRoute } from "@tanstack/react-router"

import { UsageScreen } from "@/features/models/usage-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/usage")({
  component: () => (
    <>
      <SettingsSidebar />
      <UsageScreen />
    </>
  ),
})
