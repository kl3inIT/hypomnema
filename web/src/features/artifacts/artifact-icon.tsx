import { PanelsTopLeft } from "lucide-react"

import type { ArtifactType } from "@/api/generated/types.gen"
import { KindIcon } from "@/features/preview/kind-icon"

// A page has its own icon. Before an artifact's file name is known, use the type if it identifies a PDF or image.
const defaultName = { pdf: "file.pdf", image: "file.png", file: "" }

export function ArtifactIcon({
  type,
  name,
  className,
}: {
  type: ArtifactType
  name?: string
  className?: string
}) {
  if (type === "html") return <PanelsTopLeft className={className} aria-hidden />

  return <KindIcon name={name ?? defaultName[type]} className={className} />
}
