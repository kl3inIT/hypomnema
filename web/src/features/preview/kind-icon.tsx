import {
  File,
  FileCode,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileType,
  Image,
  Presentation,
  Table,
} from "lucide-react"

import type { PreviewKind } from "./preview-kind"

// What each preview kind is drawn as, beside its name (preview-kind.ts, kindLabel), so an Artifact's card shows a
// file as its viewer will. The icons are this app's own (issue #422 review).
const icons = {
  code: FileCode,
  image: Image,
  svg: FileImage,
  pdf: FileText,
  csv: Table,
  xlsx: FileSpreadsheet,
  markdown: FileType,
  docx: FileText,
  doc: FileText,
  text: FileText,
  pptx: Presentation,
  unsupported: File,
} satisfies Record<PreviewKind, unknown>

// KindIcon is a preview kind's icon.
export function KindIcon({ kind, className }: { kind: PreviewKind; className?: string }) {
  const Icon = icons[kind]

  return <Icon className={className} aria-hidden />
}
