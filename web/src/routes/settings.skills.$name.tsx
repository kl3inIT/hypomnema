import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { SkillScreen } from "@/features/skills/skill-screen"

export const Route = createFileRoute("/settings/skills/$name")({
  component: function SkillRoute() {
    const { name } = Route.useParams()

    return (
      <>
        <SettingsSidebar />
        <SkillScreen key={name} name={name} />
      </>
    )
  },
})
