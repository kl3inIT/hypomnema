import { useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { MoreHorizontal, Trash2 } from "lucide-react"
import { useRef, useState } from "react"

import "@/api/client"
import { listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { deleteChat } from "@/api/generated/sdk.gen"
import type { Chat } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "@/components/ui/toast"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { cn } from "@/lib/utils"

// ChatMenu is the menu of a chat's row in the sidebar (issue #484), as t3code's Sidebar has a thread's menu with
// Delete (apps/web/src/components/Sidebar.tsx, handleThreadContextMenu). Delete asks first in ConfirmDialog, as the
// Bot page's Removal does; the chat is deleted for good. Deleting the chat that is open goes to its Bot's new chat. A
// file the server could not remove is said in a toast; the chat is deleted all the same.
export function ChatMenu({
  chat,
  current,
  className,
}: {
  chat: Chat
  current: boolean
  className?: string
}) {
  const [confirming, setConfirming] = useState(false)
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  // What the server could not remove, read after the dialog's run and said after it closes.
  const left = useRef<string[]>([])

  return (
    <>
      <DropdownMenu>
        <span className={cn("flex has-[[data-popup-open]]:opacity-100", className)}>
          <DropdownMenuTrigger
            render={
              <Button variant="ghost" size="icon-xs" aria-label={`Actions for ${chat.title}`} />
            }
          >
            <MoreHorizontal />
          </DropdownMenuTrigger>
        </span>
        <DropdownMenuContent align="end" width="content">
          <DropdownMenuItem variant="destructive" onClick={() => setConfirming(true)}>
            <Trash2 /> Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={`Delete “${chat.title}”?`}
        consequence="This cannot be undone. Runs started here stay."
        action="Delete"
        run={async () => {
          left.current = (await deleteChat({ path: { chat_id: chat.id } })).data.files_left
        }}
        onDone={async () => {
          if (current) await navigate({ to: "/", search: { bot: chat.bot } })
          await queryClient.invalidateQueries({ queryKey: listChatsQueryKey(), exact: true })
          if (left.current.length > 0) {
            toast.add({
              type: "warning",
              title: "Some of the chat's files stayed on the server",
              description: left.current.join("\n"),
            })
          }
        }}
        done="Chat deleted"
      />
    </>
  )
}
