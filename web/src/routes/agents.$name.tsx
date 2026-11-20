import { createFileRoute, redirect } from "@tanstack/react-router"

// The old address of an agent leads to its place in Settings (issue #857).
export const Route = createFileRoute("/agents/$name")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/settings/agents/$name", params, replace: true })
  },
})
