import { X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { cn } from "@/lib/utils"

import type { useUploads } from "./upload"

export function UploadList({ uploads }: { uploads: ReturnType<typeof useUploads> }) {
  if (uploads.items.length === 0) return null

  return (
    <ul
      aria-label="Uploads"
      className="flex max-h-48 shrink-0 flex-col gap-1 overflow-y-auto border-t p-2 text-xs"
    >
      {uploads.items.map((item) => (
        <li key={item.id} className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate" title={item.path}>
              {item.name}
            </span>
            <span
              className={cn(
                "shrink-0 text-muted-foreground",
                item.state === "failed" && "text-destructive",
              )}
            >
              {item.state === "sending"
                ? `${Math.floor((100 * item.sent) / Math.max(1, item.size))}%`
                : item.state === "done"
                  ? "Uploaded"
                  : item.state === "asking"
                    ? "Already there"
                    : "Not uploaded"}
            </span>
            {item.state === "sending" ? (
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={`Cancel ${item.name}`}
                onClick={() => uploads.cancel(item.id)}
              >
                <X />
              </Button>
            ) : item.state !== "asking" ? (
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={`Dismiss ${item.name}`}
                onClick={() => uploads.dismiss(item.id)}
              >
                <X />
              </Button>
            ) : null}
          </div>
          {item.state === "sending" && (
            <Progress value={(100 * item.sent) / Math.max(1, item.size)} aria-label={item.name} />
          )}
          {item.state === "asking" && (
            <div className="flex items-center gap-1">
              <span className="min-w-0 flex-1 text-muted-foreground">Replace the file there?</span>
              <Button size="xs" variant="outline" onClick={() => uploads.keep(item)}>
                Keep it
              </Button>
              <Button size="xs" variant="destructive" onClick={() => uploads.replace(item)}>
                Replace
              </Button>
            </div>
          )}
          {item.state === "failed" && item.problem && (
            <span className="text-muted-foreground">{item.problem}</span>
          )}
        </li>
      ))}
    </ul>
  )
}
