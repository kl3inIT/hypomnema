import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { NotesModelSettingsScreen } from "@/features/meetings/meetings-settings-screen"

export const Route = createFileRoute("/settings/notes-model")({
  component: () => (
    <>
      <SettingsSidebar />
      <NotesModelSettingsScreen />
    </>
  ),
})
