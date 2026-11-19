import { createFileRoute, redirect } from "@tanstack/react-router"

// The Agents moved into Settings (issue #857); the old address, in bookmarks and in chats, leads there, as OpenClaw
// keeps a page's old path as an alias of its place under /settings (ui/src/app-route-paths.ts, "/nodes" for
// "/settings/devices").
export const Route = createFileRoute("/agents/")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/agents", replace: true })
  },
})
