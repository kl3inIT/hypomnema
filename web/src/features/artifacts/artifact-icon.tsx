import { File, FileText, Image, PanelsTopLeft } from "lucide-react"

import type { ArtifactType } from "@/api/generated/types.gen"

// ArtifactIcon is what an artifact is, at a glance: a page, a PDF, an image or a file.
export function ArtifactIcon({ type, className }: { type: ArtifactType; className?: string }) {
  switch (type) {
    case "html":
      return <PanelsTopLeft className={className} aria-hidden />
    case "pdf":
      return <FileText className={className} aria-hidden />
    case "image":
      return <Image className={className} aria-hidden />
    default:
      return <File className={className} aria-hidden />
  }
}
