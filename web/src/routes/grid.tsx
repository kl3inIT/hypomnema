import { createFileRoute } from "@tanstack/react-router"

import { GridScreen } from "@/features/workbench/grid-screen"

export const Route = createFileRoute("/grid")({
  component: GridScreen,
})
