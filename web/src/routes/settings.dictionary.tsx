import { createFileRoute, redirect } from "@tanstack/react-router"

// The Dictionary moved out of Settings to the meetings' settings (issue #1121); the old address leads there, as
// /machines leads into Settings (routes/machines.tsx, after OpenClaw's route aliases).
export const Route = createFileRoute("/settings/dictionary")({
  beforeLoad: () => {
    throw redirect({ to: "/meetings/settings", hash: "dictionary", replace: true })
  },
})
