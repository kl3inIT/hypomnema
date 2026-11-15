import { useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { MoreHorizontal, Trash2 } from "lucide-react"
import { type ReactNode, useRef, useState } from "react"

import "@/api/client"
import { listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { deleteChat } from "@/api/generated/sdk.gen"
import type { Chat } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "@/components/ui/toast"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { cn } from "@/lib/utils"

// ChatMenu is the menu of a chat's row in the sidebar (issue #484), as t3code's Sidebar has a thread's menu with
// Delete (apps/web/src/components/Sidebar.tsx, handleThreadContextMenu). Delete asks first in ConfirmDialog, as the
// Bot page's Removal does; the chat is deleted for good. Deleting the chat that is open goes to its Bot's new chat. A
// file the server could not remove is said in a toast; the chat is deleted all the same. On the row of a Bot that
// stands for its one chat (issue #503) the menu is the Bot's (issue #470): its own items come first, named by label,
// and Delete says it deletes the chat, as OpenMausBot's Bot menu has Pin and its thread's "Delete thread".
export function ChatMenu({
  chat,
  current,
  className,
  label = `Actions for ${chat.title}`,
  children,
}: {
  chat: Chat
  current: boolean
  className?: string
  label?: string
  children?: ReactNode
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
            render={<Button variant="ghost" size="icon-xs" aria-label={label} />}
          >
            <MoreHorizontal />
          </DropdownMenuTrigger>
        </span>
        <DropdownMenuContent align="end" width="content">
          {children && (
            <>
              {children}
              <DropdownMenuSeparator />
            </>
          )}
          <DropdownMenuItem variant="destructive" onClick={() => setConfirming(true)}>
            <Trash2 /> {children ? "Delete chat" : "Delete"}
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
