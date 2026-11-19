import { createFileRoute } from "@tanstack/react-router"

import { ProfileScreen } from "@/features/agents/profile-screen"
import { SettingsSidebar } from "@/components/settings-page"

export const Route = createFileRoute("/settings/agents/$name")({
  component: AgentRoute,
})

function AgentRoute() {
  const { name } = Route.useParams()

  return (
    <>
      <SettingsSidebar />
      <ProfileScreen key={name} name={name} />
    </>
  )
}
