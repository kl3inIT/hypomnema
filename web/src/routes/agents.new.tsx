import { createFileRoute } from "@tanstack/react-router"

import { NewAgentScreen } from "@/features/agents/profile-screen"
import { AppSidebar } from "@/features/shell/app-shell"

// from names the agent a new one starts as a copy of (issue #200).
type Search = { from?: string }

export const Route = createFileRoute("/agents/new")({
  validateSearch: (s: Record<string, unknown>): Search =>
    typeof s.from === "string" && s.from ? { from: s.from } : {},
  component: NewAgent,
})

function NewAgent() {
  const { from } = Route.useSearch()

  return (
    <>
      <AppSidebar />
      <NewAgentScreen from={from} />
    </>
  )
}
