import { createFileRoute, redirect } from "@tanstack/react-router"

// The Machines moved into Settings (issue #857); the old address leads there, as OpenClaw keeps "/nodes" as an alias
// of "/settings/devices" (ui/src/app-route-paths.ts). A machine's terminal keeps its own page,
// /machines/<node>/terminal, outside Settings' frame.
export const Route = createFileRoute("/machines")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/machines", replace: true })
  },
})
