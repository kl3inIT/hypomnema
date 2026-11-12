import { createFileRoute } from "@tanstack/react-router"

import { ChannelsScreen } from "@/features/settings/channels-screen"
import { SettingsSidebar } from "@/features/settings/settings-shell"

export const Route = createFileRoute("/settings/channels")({
  component: () => (
    <>
      <SettingsSidebar />
      <ChannelsScreen />
    </>
  ),
})
