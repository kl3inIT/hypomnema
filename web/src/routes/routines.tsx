import { createFileRoute } from "@tanstack/react-router"

import { RoutinesScreen } from "@/features/routines/routines-screen"
import { AppSidebar } from "@/features/shell/app-shell"

type Search = { bot?: string; open?: string }

// The Routines page keeps its view in the URL, as the Team page does: the Bot it shows and the open Routine.
export const Route = createFileRoute("/routines")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    bot: typeof s.bot === "string" && s.bot ? s.bot : undefined,
    open: typeof s.open === "string" && s.open ? s.open : undefined,
  }),
  component: RoutinesRoute,
})

function RoutinesRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <AppSidebar />
      <RoutinesScreen
        bot={search.bot ?? ""}
        open={search.open}
        onChange={(next) =>
          void navigate({
            search: (prev) => ({
              ...prev,
              ...("bot" in next ? { bot: next.bot || undefined, open: undefined } : {}),
              ...("open" in next ? { open: next.open } : {}),
            }),
            replace: "bot" in next,
          })
        }
      />
    </>
  )
}
