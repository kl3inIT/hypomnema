import { createFileRoute } from "@tanstack/react-router"

import { BotScreen } from "@/features/bots/bot-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/bots/$name")({
  component: BotRoute,
})

function BotRoute() {
  const { name } = Route.useParams()

  return (
    <>
      <AppSidebar />
      <BotScreen key={name} name={name} />
    </>
  )
}
