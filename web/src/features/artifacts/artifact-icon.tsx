import { PanelsTopLeft } from "lucide-react"

import type { ArtifactRevision, ArtifactType } from "@/api/generated/types.gen"
import { KindIcon } from "@/features/preview/kind-icon"

import { kindOf } from "./artifact-state"

// ArtifactIcon is what an artifact is, at a glance: a page, or the kind of file its viewer shows (issue #422), from
// its revision's file when it is known and from its type until then.
export function ArtifactIcon({
  type,
  revision,
  className,
}: {
  type: ArtifactType
  revision?: ArtifactRevision
  className?: string
}) {
  if (type === "html") return <PanelsTopLeft className={className} aria-hidden />

  return <KindIcon kind={kindOf(type, revision)} className={className} />
}
