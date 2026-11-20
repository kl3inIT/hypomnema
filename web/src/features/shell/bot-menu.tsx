import { useQueryClient } from "@tanstack/react-query"
import { FolderMinus, FolderPlus, MoreHorizontal, Star } from "lucide-react"
import type { ReactNode } from "react"

import "@/api/client"
import { listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useMoveBot, useSetMainChat } from "@/features/bots/use-bots"
import { useDepartments } from "@/features/bots/use-departments"
import { everyCall } from "@/lib/query-keys"
import { cn } from "@/lib/utils"

// MoveItem moves a Bot between departments from its row's menu (issue #471), as OpenMausBot's Bot menu has "Move to
// department" with a flyout: every department, the Bot's own one turned off, then "New department…" and, for a Bot
// in one, "Remove from department" (src/components/SidebarBotMenu.tsx, sidebar.bot.moveToSection,
// sidebar.section.newContext and sidebar.section.removeFromDepartment; Apache License 2.0).
export function MoveItem({
  bot,
  department,
  onNewDepartment,
}: {
  bot: string
  department?: string
  onNewDepartment: (bot: string) => void
}) {
  const departments = useDepartments()
  const move = useMoveBot()
  const list = departments.data ?? []

  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <FolderPlus /> Move to department
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        {list.map((d) => (
          <DropdownMenuItem
            key={d.id}
            disabled={d.id === department}
            onClick={() => move.mutate({ id: bot, department: d.id })}
          >
            {d.name}
          </DropdownMenuItem>
        ))}
        {list.length > 0 && <DropdownMenuSeparator />}
        <DropdownMenuItem onClick={() => onNewDepartment(bot)}>
          <FolderPlus /> New department…
        </DropdownMenuItem>
        {department && (
          <DropdownMenuItem onClick={() => move.mutate({ id: bot, department: "" })}>
            <FolderMinus /> Remove from department
          </DropdownMenuItem>
        )}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}

// SetMainItem makes the chat being viewed its Bot's main chat (issue #923), as OpenMausBot's Bot menu has "Set as
// main thread" with its star, disabled with "This is already the main thread" on the main one
// (src/components/SidebarBotMenu.tsx setMain, src/locales/en.json; Apache License 2.0). The hint is a line under the
// item, since a disabled item takes no pointer for a tooltip.
export function SetMainItem({ bot, chat, isMain }: { bot: string; chat: string; isMain: boolean }) {
  const setMain = useSetMainChat()
  const queryClient = useQueryClient()

  return (
    <DropdownMenuItem
      disabled={isMain}
      onClick={() =>
        setMain.mutate(
          { id: bot, chat },
          // The chats are read again: the first page carries every Bot's main chat (issue #471).
          {
            onSettled: () =>
              void queryClient.invalidateQueries({ queryKey: everyCall(listChatsQueryKey()) }),
          },
        )
      }
    >
      <Star />
      <span className="flex flex-col">
        Set as main chat
        {isMain && (
          <span className="text-xs text-muted-foreground">This is already the main chat</span>
        )}
      </span>
    </DropdownMenuItem>
  )
}

// BotMenu is the menu of a Bot's row that stands for no one chat, drawn as a chat row's menu (chat-menu.tsx), with
// the items given.
export function BotMenu({
  name,
  className,
  children,
}: {
  name: string
  className?: string
  children: ReactNode
}) {
  return (
    <DropdownMenu>
      <span className={cn("flex has-[[data-popup-open]]:opacity-100", className)}>
        <DropdownMenuTrigger
          render={<Button variant="ghost" size="icon-xs" aria-label={`Actions for ${name}`} />}
        >
          <MoreHorizontal />
        </DropdownMenuTrigger>
      </span>
      <DropdownMenuContent align="end" width="content">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
