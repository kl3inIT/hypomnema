import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { DictionarySettingsScreen } from "@/features/meetings/meetings-settings-screen"

export const Route = createFileRoute("/settings/dictionary")({
  component: () => (
    <>
      <SettingsSidebar />
      <DictionarySettingsScreen />
    </>
  ),
})
