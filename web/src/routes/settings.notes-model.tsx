import { createFileRoute, redirect } from "@tanstack/react-router"

// The Notes model moved out of Settings to the meetings' settings (issue #1121); the old address leads there, as
// /machines leads into Settings (routes/machines.tsx, after OpenClaw's route aliases).
export const Route = createFileRoute("/settings/notes-model")({
  beforeLoad: () => {
    throw redirect({ to: "/meetings/settings", hash: "notes-model", replace: true })
  },
})
