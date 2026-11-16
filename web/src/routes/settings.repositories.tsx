import { createFileRoute } from "@tanstack/react-router"

import { RepositoriesScreen } from "@/features/settings/repositories-screen"
import { SettingsSidebar } from "@/components/settings-page"

type Search = { host?: string }

// The Repositories page keeps the git host it is narrowed to in the URL, so that a host's row under Integrations
// can link to its repositories (issue #647).
export const Route = createFileRoute("/settings/repositories")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    host: typeof s.host === "string" && s.host ? s.host : undefined,
  }),
  component: RepositoriesRoute,
})

function RepositoriesRoute() {
  const { host } = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <SettingsSidebar />
      <RepositoriesScreen
        host={host}
        onHost={(next) => void navigate({ search: { host: next }, replace: true })}
      />
    </>
  )
}
