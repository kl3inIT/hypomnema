import { createFileRoute } from "@tanstack/react-router"

import { MeetingScreen } from "@/features/meetings/meeting-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/meetings_/$meetingId")({
  component: MeetingRoute,
})

function MeetingRoute() {
  const { meetingId } = Route.useParams()

  return (
    <>
      <AppSidebar />
      <MeetingScreen key={meetingId} id={meetingId} />
    </>
  )
}
