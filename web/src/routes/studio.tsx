import { createFileRoute } from "@tanstack/react-router"

import { AppSidebar } from "@/features/shell/app-shell"
import { StudioScreen } from "@/features/studio/studio-screen"

export const Route = createFileRoute("/studio")({
  component: () => (
    <>
      <AppSidebar />
      <StudioScreen />
    </>
  ),
})
