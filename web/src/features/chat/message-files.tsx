import { FileText, ImageOff } from "lucide-react"
import { useState } from "react"

import type { ChatAttachment } from "@/api/generated/types.gen"
import {
  Attachment,
  AttachmentContent,
  AttachmentDescription,
  AttachmentMedia,
  AttachmentTitle,
  AttachmentTrigger,
} from "@/components/ui/attachment"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { imageType, useImage, useObjectURL } from "@/features/files/files-state"
import { cn } from "@/lib/utils"

import { botFolder, formatSize } from "./attachments"
import { useOpenFile } from "./open-file-context"

// The files of the owner's message (issue #343), as t3code's TimelineUserMessageRow.tsx shows a message's images
// above its text, one alone 12rem high and several in a grid, each opening large in a dialog
// (ExpandedImageDialog.tsx, ChatMarkdownMedia.tsx; MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code), and its other files as chips with name and size, as OpenMausBot's
// AttachmentGallery.tsx (Apache-2.0) and assistant-ui's message-attachment.tsx (MIT) do. An image's bytes are read
// from the Bot's folder through the Files panel's raw read; a file's chip opens it in the pane, beside the Files
// panel.
export function MessageFiles({ chat, files }: { chat: string; files: ChatAttachment[] }) {
  const images = files.filter((file) => imageType(file.path) !== undefined)
  const others = files.filter((file) => imageType(file.path) === undefined)

  return (
    <>
      {images.length > 0 && (
        <ul
          aria-label="Attached images"
          className={cn(
            "flex max-w-[80%] flex-wrap justify-end gap-1.5",
            images.length > 1 && "grid grid-cols-2",
          )}
        >
          {images.map((file) => (
            <li key={file.path}>
              <Thumbnail chat={chat} file={file} alone={images.length === 1} />
            </li>
          ))}
        </ul>
      )}
      {others.length > 0 && (
        <ul aria-label="Attached files" className="flex max-w-[80%] flex-wrap justify-end gap-1.5">
          {others.map((file) => (
            <li key={file.path}>
              <FileChip file={file} />
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

function Thumbnail({ chat, file, alone }: { chat: string; file: ChatAttachment; alone: boolean }) {
  const [open, setOpen] = useState(false)
  const image = useImage(chat, botFolder, file.path)
  const src = useObjectURL(image.data)
  const box = alone ? "h-48 min-w-24 max-w-full" : "aspect-[4/3] w-full"

  if (image.isError) {
    return (
      <div
        className={cn(
          "flex items-center justify-center gap-1.5 rounded-2xl border px-3 text-xs text-muted-foreground",
          alone ? "h-16" : "aspect-[4/3] w-full",
        )}
      >
        <ImageOff className="size-4" aria-hidden />
        <span className="truncate">{file.name} could not be read</span>
      </div>
    )
  }

  // Its bytes are on their way: the place it will take, pulsing as a Skeleton does.
  if (!src) {
    return (
      <div
        aria-busy="true"
        aria-label={`Loading ${file.name}`}
        className={cn("animate-pulse rounded-2xl bg-muted", alone ? "h-48 w-64 max-w-full" : box)}
      />
    )
  }

  return (
    <>
      {/* A picture that is pressed, not a button with a label: its own design (docs/conventions.md). */}
      <button
        type="button"
        aria-label={`Preview ${file.name}`}
        className="block cursor-zoom-in overflow-hidden rounded-2xl border focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        onClick={() => setOpen(true)}
      >
        <img
          src={src}
          alt={file.name}
          decoding="async"
          className={cn("block object-cover", alone ? "h-48 w-auto max-w-full min-w-24" : box)}
        />
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent size="image">
          <DialogHeader>
            <DialogTitle>{file.name}</DialogTitle>
            <DialogDescription>{formatSize(file.size)}</DialogDescription>
          </DialogHeader>
          <img
            src={src}
            alt={file.name}
            className="block max-h-[calc(100dvh-8rem)] max-w-[calc(92vw-1rem)] rounded-lg object-contain"
          />
        </DialogContent>
      </Dialog>
    </>
  )
}

function FileChip({ file }: { file: ChatAttachment }) {
  const openFile = useOpenFile()

  return (
    <div className="max-w-64">
      <Attachment size="sm" title={file.path}>
        <AttachmentMedia>
          <FileText />
        </AttachmentMedia>
        <AttachmentContent>
          <AttachmentTitle>{file.name}</AttachmentTitle>
          <AttachmentDescription>{formatSize(file.size)}</AttachmentDescription>
        </AttachmentContent>
        {openFile && (
          <AttachmentTrigger
            aria-label={`Open ${file.name}`}
            onClick={() => openFile(botFolder, file.path)}
          />
        )}
      </Attachment>
    </div>
  )
}
