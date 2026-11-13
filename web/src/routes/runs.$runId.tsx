import { createFileRoute } from "@tanstack/react-router"

import { TaskPanel } from "@/features/tasks/task-panel"
import { RunScreen } from "@/features/runs/run-screen"
import { AppSidebar } from "@/features/shell/app-shell"

type Search = { task?: string }

export const Route = createFileRoute("/runs/$runId")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    task: typeof s.task === "string" && s.task ? s.task : undefined,
  }),
  component: RunRoute,
})

function RunRoute() {
  const { runId } = Route.useParams()
  const { task } = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <AppSidebar />
      <RunScreen
        key={runId}
        runId={runId}
        onOpenTask={(id) => void navigate({ search: { task: id } })}
      />
      <TaskPanel taskId={task} onClose={() => void navigate({ search: {} })} />
    </>
  )
}
