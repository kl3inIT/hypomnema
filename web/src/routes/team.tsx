import { createFileRoute } from "@tanstack/react-router"

import { AppSidebar } from "@/features/shell/app-shell"
import { TaskPanel } from "@/features/tasks/task-panel"
import { TeamScreen } from "@/features/team/team-screen"

type Search = { task?: string }

// The Team page keeps the open Task in the URL, so a link or a reload opens it again.
export const Route = createFileRoute("/team")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    task: typeof s.task === "string" && s.task ? s.task : undefined,
  }),
  component: TeamRoute,
})

function TeamRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <AppSidebar />
      <TeamScreen
        task={search.task}
        onOpenTask={(task) => void navigate({ search: (prev) => ({ ...prev, task }) })}
      />
      <TaskPanel
        taskId={search.task}
        onClose={() => void navigate({ search: (prev) => ({ ...prev, task: undefined }) })}
      />
    </>
  )
}
