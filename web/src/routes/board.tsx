import { createFileRoute } from "@tanstack/react-router"

import { BoardScreen } from "@/features/board/board-screen"
import type { Group } from "@/features/board/board-state"
import { TaskPanel } from "@/features/tasks/task-panel"
import { AppSidebar } from "@/features/shell/app-shell"

type Search = { group?: Group; repo?: string; task?: string }

const groups: Group[] = ["status", "run", "repo"]

// The board keeps its view in the URL: the grouping, the repository filter and the open Task.
export const Route = createFileRoute("/board")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    group:
      groups.includes(s.group as Group) && s.group !== "status" ? (s.group as Group) : undefined,
    repo: typeof s.repo === "string" && s.repo ? s.repo : undefined,
    task: typeof s.task === "string" && s.task ? s.task : undefined,
  }),
  component: BoardRoute,
})

function BoardRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <AppSidebar />
      <BoardScreen
        group={search.group ?? "status"}
        repo={search.repo ?? ""}
        onChange={(next) =>
          void navigate({
            search: (prev) => ({
              ...prev,
              ...("group" in next
                ? { group: next.group === "status" ? undefined : next.group }
                : {}),
              ...("repo" in next ? { repo: next.repo || undefined } : {}),
            }),
            replace: true,
          })
        }
        onOpen={(task) => void navigate({ search: (prev) => ({ ...prev, task }) })}
      />
      <TaskPanel
        taskId={search.task}
        onClose={() => void navigate({ search: (prev) => ({ ...prev, task: undefined }) })}
      />
    </>
  )
}
