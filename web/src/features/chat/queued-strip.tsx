// Adapted from Omnigent web/src/pages/QueuedMessagesStrip.tsx (Apache License 2.0,
// https://github.com/omnigent-ai/omnigent): the strip docked above the composer, one row per message queued while
// the agent works, with Edit (back into the composer), Send now (a steer) and Remove, always shown. Reordering
// and the host-waiting states are left out (issue #497).
import { ArrowUp, Clock, ImageIcon, Paperclip, Pencil, X } from "lucide-react"

import type { QueuedChatMessage } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

export function QueuedStrip({
  queue,
  onEdit,
  onSendNow,
  onRemove,
}: {
  queue: readonly QueuedChatMessage[]
  onEdit: (m: QueuedChatMessage) => void
  onSendNow: (m: QueuedChatMessage) => void
  onRemove: (m: QueuedChatMessage) => void
}) {
  if (queue.length === 0) return null

  return (
    <div
      data-testid="queued-strip"
      className="mx-auto -mb-4 flex w-full max-w-(--chat-content-max-width) flex-col rounded-t-2xl border border-b-0 border-(--composer-outline,var(--border)) bg-muted/60 px-2 pt-1.5 pb-5.5 md:px-3"
    >
      {/* About five rows tall, then it scrolls, so a long queue never pushes the composer off the screen. */}
      <ul
        aria-label="Queued messages"
        className="flex max-h-32 flex-col gap-1 overflow-y-auto overscroll-contain"
      >
        {queue.map((m) => (
          <QueuedRow
            key={m.id}
            message={m}
            onEdit={onEdit}
            onSendNow={onSendNow}
            onRemove={onRemove}
          />
        ))}
      </ul>
    </div>
  )
}

function QueuedRow({
  message,
  onEdit,
  onSendNow,
  onRemove,
}: {
  message: QueuedChatMessage
  onEdit: (m: QueuedChatMessage) => void
  onSendNow: (m: QueuedChatMessage) => void
  onRemove: (m: QueuedChatMessage) => void
}) {
  const text = message.text.trim()
  const files = message.attachments ?? []
  const FileIcon = files.every((f) => f.type?.startsWith("image/")) ? ImageIcon : Paperclip

  return (
    <li className="flex min-w-0 items-center gap-1 py-0.5 text-sm">
      <Clock className="mx-1 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="flex min-w-0 flex-1 items-center gap-2 max-md:flex-col max-md:items-start max-md:gap-0.5">
        {text && (
          <span className="max-w-full min-w-0 truncate" title={message.text}>
            {message.text}
          </span>
        )}
        {files.length > 0 && (
          <span
            title={files.map((f) => f.name).join("\n")}
            className={cn(
              "flex max-w-[min(16rem,100%)] min-w-0",
              text && "md:max-w-[min(14rem,55%)]",
            )}
          >
            <Badge variant="outline">
              <FileIcon aria-hidden="true" />
              <span className="min-w-6 truncate">{files[0]?.name}</span>
              {files.length > 1 && (
                <span className="shrink-0 tabular-nums">+{files.length - 1}</span>
              )}
            </Badge>
          </span>
        )}
      </div>
      <span className="flex shrink-0 items-center">
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label="Edit queued message"
          title="Edit"
          onClick={() => onEdit(message)}
        >
          <Pencil className="size-3.5" aria-hidden="true" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label="Send queued message now"
          title="Send now"
          onClick={() => onSendNow(message)}
        >
          <ArrowUp className="size-4" aria-hidden="true" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label="Remove queued message"
          title="Remove"
          onClick={() => onRemove(message)}
        >
          <X className="size-4" aria-hidden="true" />
        </Button>
      </span>
    </li>
  )
}
