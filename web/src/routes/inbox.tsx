import { createFileRoute } from "@tanstack/react-router"

import { InboxScreen } from "@/features/inbox/inbox-screen"
import { AppSidebar } from "@/features/shell/app-shell"

type Search = { item?: string }

// The Inbox keeps the selected item in the URL, as the Team page keeps its open Task, so a link or a reload opens
// it again.
export const Route = createFileRoute("/inbox")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    item: typeof s.item === "string" && s.item ? s.item : undefined,
  }),
  component: InboxRoute,
})

function InboxRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <AppSidebar />
      <InboxScreen
        selected={search.item}
        onSelect={(item) => void navigate({ search: (prev) => ({ ...prev, item }), replace: true })}
      />
    </>
  )
}
