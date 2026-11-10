import { createFileRoute } from "@tanstack/react-router"
import { useEffect } from "react"

import { TerminalScreen } from "@/features/machines/terminal-screen"

type Search = { reconnect?: string }

// The session's id rides in the address, as Coder's ?reconnect=: a reload returns to the same shell.
export const Route = createFileRoute("/machines_/$nodeId/terminal")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    reconnect: typeof s.reconnect === "string" && s.reconnect ? s.reconnect : undefined,
  }),
  component: TerminalRoute,
})

function TerminalRoute() {
  const { nodeId } = Route.useParams()
  const { reconnect } = Route.useSearch()
  const navigate = Route.useNavigate()

  const newSession = () =>
    void navigate({ search: { reconnect: crypto.randomUUID() }, replace: true })

  // A page opened without a session makes one and puts it in the address.
  useEffect(() => {
    if (!reconnect) void navigate({ search: { reconnect: crypto.randomUUID() }, replace: true })
  }, [reconnect, navigate])

  if (!reconnect) return null

  return (
    <TerminalScreen
      key={reconnect}
      nodeId={nodeId}
      sessionId={reconnect}
      onNewSession={newSession}
    />
  )
}
