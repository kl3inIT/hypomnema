import { createFileRoute } from "@tanstack/react-router"

import { MeetingSettingsScreen } from "@/features/meetings/meetings-settings-screen"
import { AppSidebar } from "@/features/shell/app-shell"

// The meetings' settings, opened from the gear on the Meetings page (issue #1121).
export const Route = createFileRoute("/meetings_/settings")({
  component: () => (
    <>
      <AppSidebar />
      <MeetingSettingsScreen />
    </>
  ),
})
