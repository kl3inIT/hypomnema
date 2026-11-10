import { createFileRoute } from "@tanstack/react-router"

import { RunsScreen } from "@/features/runs/runs-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/runs/")({
  component: () => (
    <>
      <AppSidebar />
      <RunsScreen />
    </>
  ),
})
