import { FileText, X } from "lucide-react"

import {
  Attachment,
  AttachmentAction,
  AttachmentActions,
  AttachmentContent,
  AttachmentDescription,
  AttachmentGroup,
  AttachmentMedia,
  AttachmentTitle,
} from "@/components/ui/attachment"
import { Spinner } from "@/components/ui/spinner"

import { formatSize, type Attachments } from "./attachments"

// The files of the message being written, as chips above the box: t3code's ComposerAttachmentStrip.tsx (MIT,
// Copyright (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code) for the name with its size, or its upload's
// progress, and a remove button a chip; an image shows its thumbnail, as t3code's ComposerImageThumbnail.tsx, Omnigent's
// ComposerAttachments.tsx (Apache-2.0) and AI Elements' attachments.tsx (Apache-2.0) do.

export function AttachmentStrip({ attachments }: { attachments: Attachments }) {
  if (attachments.items.length === 0) return null

  return (
    <div className="px-3 pt-3">
      <AttachmentGroup aria-label="Files to send">
        {attachments.items.map((item) => {
          const percent = Math.floor((100 * item.sent) / Math.max(1, item.size))

          return (
            <div key={item.id} className="max-w-56 flex-none snap-start">
              <Attachment
                size="sm"
                state={
                  item.state === "uploading"
                    ? "uploading"
                    : item.state === "failed"
                      ? "error"
                      : "done"
                }
                title={item.problem ?? item.name}
              >
                <AttachmentMedia variant={item.preview ? "image" : "icon"}>
                  {item.preview ? (
                    <img src={item.preview} alt="" />
                  ) : item.state === "uploading" ? (
                    <Spinner />
                  ) : (
                    <FileText />
                  )}
                </AttachmentMedia>
                <AttachmentContent>
                  <AttachmentTitle>{item.name}</AttachmentTitle>
                  <AttachmentDescription>
                    {item.state === "uploading"
                      ? `Uploading ${percent}%`
                      : item.state === "failed"
                        ? "Not uploaded"
                        : formatSize(item.size)}
                  </AttachmentDescription>
                </AttachmentContent>
                <AttachmentActions>
                  <AttachmentAction
                    aria-label={`Remove ${item.name}`}
                    onClick={() => attachments.draft.remove(item.id)}
                  >
                    <X />
                  </AttachmentAction>
                </AttachmentActions>
              </Attachment>
            </div>
          )
        })}
      </AttachmentGroup>
    </div>
  )
}
