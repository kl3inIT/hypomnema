import { createFileRoute } from "@tanstack/react-router"

import { AppSidebar } from "@/features/shell/app-shell"
import { StudioTakeScreen } from "@/features/studio/studio-item-screen"

export const Route = createFileRoute("/studio_/takes/$takeId")({
  component: StudioTakeRoute,
})

function StudioTakeRoute() {
  const { takeId } = Route.useParams()

  return (
    <>
      <AppSidebar />
      <StudioTakeScreen key={takeId} id={takeId} />
    </>
  )
}
