import { createFileRoute } from "@tanstack/react-router"

import { BotScreen } from "@/features/bots/bot-screen"
import { AppSidebar } from "@/features/shell/app-shell"

// A Bot's page is at its id, which never changes, so a rename keeps its address (issue #466).
export const Route = createFileRoute("/bots/$id")({
  component: BotRoute,
})

function BotRoute() {
  const { id } = Route.useParams()

  return (
    <>
      <AppSidebar />
      <BotScreen key={id} id={id} />
    </>
  )
}
