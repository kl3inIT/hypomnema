import { createFileRoute } from "@tanstack/react-router"
import { useEffect } from "react"

import { rememberPlace } from "@/features/shell/last-place"
import { TaskPanel } from "@/features/tasks/task-panel"
import { GridScreen } from "@/features/workbench/grid-screen"

type Search = { task?: string }

// The grid keeps the Task a chat's wake-up opened in its URL, as a chat's page does (issue #303).
export const Route = createFileRoute("/grid")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    task: typeof s.task === "string" && s.task ? s.task : undefined,
  }),
  component: GridRoute,
})

function GridRoute() {
  useEffect(() => rememberPlace({ kind: "grid" }), [])
  const search = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <GridScreen />
      <TaskPanel
        taskId={search.task}
        onClose={() => void navigate({ search: (prev) => ({ ...prev, task: undefined }) })}
      />
    </>
  )
}
