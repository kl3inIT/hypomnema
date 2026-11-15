import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { SkillsScreen } from "@/features/skills/skills-screen"

export const Route = createFileRoute("/settings/skills/")({
  component: () => (
    <>
      <SettingsSidebar />
      <SkillsScreen />
    </>
  ),
})
