import { createFileRoute, redirect } from "@tanstack/react-router"

// The old address of a new agent leads to its place in Settings, with the agent it copies (issue #857).
export const Route = createFileRoute("/agents/new")({
  validateSearch: (s: Record<string, unknown>): { from?: string } =>
    typeof s.from === "string" && s.from ? { from: s.from } : {},
  beforeLoad: ({ search }) => {
    throw redirect({ to: "/settings/agents/new", search, replace: true })
  },
})
