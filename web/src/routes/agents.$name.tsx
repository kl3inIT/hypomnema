import { createFileRoute } from "@tanstack/react-router"

import { ProfileScreen } from "@/features/agents/profile-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/agents/$name")({
  component: AgentRoute,
})

function AgentRoute() {
  const { name } = Route.useParams()

  return (
    <>
      <AppSidebar />
      <ProfileScreen key={name} name={name} />
    </>
  )
}
