import { Download, File } from "lucide-react"

import { buttonVariants } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"

// DownloadView is a file this app does not draw: the reason, and the download that holds the whole file. Copied
// from MemoryOS (web/src/features/preview/download-view.tsx), in this app's empty state as #402's download was.
// The server sends the download as an attachment, so it is saved, never opened as a page of this origin.
export function DownloadView({
  href,
  filename,
  message,
}: {
  href: string
  filename: string
  message: string
}) {
  return (
    <div className="flex min-h-0 flex-1 flex-col justify-center">
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <File />
          </EmptyMedia>
          <EmptyTitle>
            <span className="break-all">{filename}</span>
          </EmptyTitle>
          <EmptyDescription>{message}</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <a href={href} download={filename} className={buttonVariants({ variant: "default" })}>
            <Download />
            Download
          </a>
        </EmptyContent>
      </Empty>
    </div>
  )
}
