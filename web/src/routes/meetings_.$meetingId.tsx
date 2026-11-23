import { createFileRoute } from "@tanstack/react-router"

import { MeetingScreen } from "@/features/meetings/meeting-screen"
import { AppSidebar } from "@/features/shell/app-shell"

// line opens the meeting at a line of its transcript, as an Inbox item from a meeting links its action (issue #1105).
type Search = { line?: number }

export const Route = createFileRoute("/meetings_/$meetingId")({
  validateSearch: (s: Record<string, unknown>): Search => {
    const line = Number(s.line)

    return Number.isInteger(line) && line > 0 ? { line } : {}
  },
  component: MeetingRoute,
})

function MeetingRoute() {
  const { meetingId } = Route.useParams()
  const { line } = Route.useSearch()

  return (
    <>
      <AppSidebar />
      <MeetingScreen key={meetingId} id={meetingId} line={line} />
    </>
  )
}
