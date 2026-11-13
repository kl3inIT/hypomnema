import { createFileRoute } from "@tanstack/react-router"
import { useEffect } from "react"

import { rememberPlace } from "@/features/shell/last-place"
import { GridScreen } from "@/features/workbench/grid-screen"

export const Route = createFileRoute("/grid")({
  component: GridRoute,
})

function GridRoute() {
  useEffect(() => rememberPlace({ kind: "grid" }), [])

  return <GridScreen />
}
