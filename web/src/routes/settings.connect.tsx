import { createFileRoute } from "@tanstack/react-router"

import { ConnectScreen } from "@/features/models/connect-screen"
import { SettingsSidebar } from "@/components/settings-page"

type Search = { key?: string }

export const Route = createFileRoute("/settings/connect")({
  // key is the gateway key the page opens with, from its row's Connect on Gateway keys (issue #907).
  validateSearch: (s: Record<string, unknown>): Search => ({
    key: typeof s.key === "string" && s.key !== "" ? s.key : undefined,
  }),
  component: function Connect() {
    const { key } = Route.useSearch()

    return (
      <>
        <SettingsSidebar />
        <ConnectScreen key={key ?? "none"} keyId={key} />
      </>
    )
  },
})
