import { createFileRoute } from "@tanstack/react-router"

import { AppSidebar } from "@/features/shell/app-shell"
import { StudioVideoScreen } from "@/features/studio/studio-item-screen"

export const Route = createFileRoute("/studio_/videos/$videoId")({
  component: StudioVideoRoute,
})

function StudioVideoRoute() {
  const { videoId } = Route.useParams()

  return (
    <>
      <AppSidebar />
      <StudioVideoScreen key={videoId} id={videoId} />
    </>
  )
}
