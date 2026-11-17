import { createFileRoute } from "@tanstack/react-router"

import { SharedArtifactView } from "@/features/artifacts/shared-view"

// A view-only link's page (issue #745), opened without signing in: the root layout shows it with no session and
// no app shell (__root.tsx), as open-webui's /s/{id}.
export const Route = createFileRoute("/s/$token")({
  component: SharedRoute,
})

function SharedRoute() {
  const { token } = Route.useParams()

  return <SharedArtifactView token={token} />
}
