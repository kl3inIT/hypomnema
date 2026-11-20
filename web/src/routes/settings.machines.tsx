import { createFileRoute } from "@tanstack/react-router"

import { MachinesScreen } from "@/features/machines/machines-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/machines")({
  component: () => (
    <>
      <SettingsSidebar />
      <MachinesScreen />
    </>
  ),
})
