import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { MeetingsSettingsScreen } from "@/features/meetings/meetings-settings-screen"

export const Route = createFileRoute("/settings/meetings")({
  component: () => (
    <>
      <SettingsSidebar />
      <MeetingsSettingsScreen />
    </>
  ),
})
