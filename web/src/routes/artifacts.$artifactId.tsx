import { createFileRoute } from "@tanstack/react-router"

import { ArtifactPanel } from "@/features/artifacts/artifact-panel"

type Search = { rev?: number }

// An artifact on a page of its own (issue #402, row 7): what ↗ opens in a new tab and the link a Lark reply carries.
// It is the app's page, signed in, with a thin header over the same locked frame, never a blob: or data: address,
// which would take the app's origin (Omnigent's codeViewerHelpers.ts, openHtmlArtifactInNewTab). The revision is
// in the address, so a link names the one it was copied at.
export const Route = createFileRoute("/artifacts/$artifactId")({
  validateSearch: (s: Record<string, unknown>): Search => {
    const rev = Number(s.rev)

    return { rev: Number.isInteger(rev) && rev > 0 ? rev : undefined }
  },
  component: ArtifactRoute,
})

function ArtifactRoute() {
  const { artifactId } = Route.useParams()
  const { rev } = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <main className="flex min-h-0 min-w-0 flex-1 flex-col">
      <ArtifactPanel
        id={artifactId}
        rev={rev}
        where="page"
        onRev={(next) => void navigate({ search: { rev: next }, replace: true })}
      />
    </main>
  )
}
