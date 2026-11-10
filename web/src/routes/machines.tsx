import { createFileRoute } from "@tanstack/react-router"

import { MachinesScreen } from "@/features/machines/machines-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/machines")({
  component: () => (
    <>
      <AppSidebar />
      <MachinesScreen />
    </>
  ),
})
