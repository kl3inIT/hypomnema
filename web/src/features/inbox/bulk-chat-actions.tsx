import { useState } from "react"

import type { InboxChat, InboxChatSource } from "@/api/generated/types.gen"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { toast } from "@/components/ui/toast"
import { errorText } from "@/lib/server-error"

import { useSetChatsBulk, useUndoChatsBulk } from "./use-chats-read"

type Choice = { reads: boolean; chat_ids: string[] }

function chatCount(count: number) {
  return `${count} ${count === 1 ? "chat" : "chats"}`
}

export function BulkChatActions({
  source,
  title,
  chats,
  disabled,
}: {
  source: InboxChatSource
  title: string
  chats: InboxChat[]
  disabled: boolean
}) {
  const [open, setOpen] = useState(false)
  const [choice, setChoice] = useState<Choice | null>(null)
  const set = useSetChatsBulk()
  const undo = useUndoChatsBulk()
  const path = { integration: source.integration, label: source.label }
  const unavailable = disabled || set.isPending || undo.isPending || !chats.length

  function choose(reads: boolean) {
    setChoice({ reads, chat_ids: chats.map((chat) => chat.id) })
    setOpen(true)
  }

  async function confirm(body: Choice) {
    try {
      const receipt = await set.mutateAsync({ path, body })
      if (!receipt.changes.length) {
        toast.add({ type: "info", title: "No chats changed" })
        return
      }

      const id = toast.add({
        type: "success",
        title: `${body.reads ? "Switched on" : "Skipped"} ${chatCount(receipt.changes.length)}`,
        timeout: 10_000,
        actionProps: {
          children: "Undo",
          onClick: async () => {
            toast.close(id)
            try {
              await undo.mutateAsync({ path, body: receipt })
              toast.add({ type: "success", title: "Previous chat choices restored" })
            } catch (error) {
              toast.add({ type: "error", title: "Could not undo", description: errorText(error) })
            }
          },
        },
      })
    } catch (error) {
      toast.add({ type: "error", title: "Could not update chats", description: errorText(error) })
    }
  }

  const action = choice?.reads ? "Read" : "Skip"
  return (
    <AlertDialog
      open={open}
      onOpenChange={setOpen}
      onOpenChangeComplete={(open) => {
        if (!open) setChoice(null)
      }}
    >
      <div className="flex flex-wrap justify-end gap-2">
        <AlertDialogTrigger
          render={<Button variant="outline" size="sm" />}
          disabled={unavailable}
          onClick={() => choose(false)}
        >
          Skip all shown
        </AlertDialogTrigger>
        <AlertDialogTrigger
          render={<Button variant="outline" size="sm" />}
          disabled={unavailable}
          onClick={() => choose(true)}
        >
          Read all shown
        </AlertDialogTrigger>
      </div>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{`${action} ${chatCount(choice?.chat_ids.length ?? 0)}?`}</AlertDialogTitle>
          <AlertDialogDescription>
            This applies to the chats shown for {title}.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              if (!choice) return
              setOpen(false)
              void confirm(choice)
            }}
          >
            {action}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
