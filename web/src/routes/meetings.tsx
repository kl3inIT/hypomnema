import { createFileRoute } from "@tanstack/react-router"

import { MeetingsScreen } from "@/features/meetings/meetings-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/meetings")({
  component: () => (
    <>
      <AppSidebar />
      <MeetingsScreen />
    </>
  ),
})
