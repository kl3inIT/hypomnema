import { FolderMinus, FolderPlus, MoreHorizontal } from "lucide-react"

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
import { useMoveBot } from "@/features/bots/use-bots"
import { useDepartments } from "@/features/bots/use-departments"
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

// BotMenu is the menu of a Bot's row that stands for no one chat, drawn as a chat row's menu (chat-menu.tsx).
export function BotMenu({
  bot,
  name,
  department,
  onNewDepartment,
  className,
}: {
  bot: string
  name: string
  department?: string
  onNewDepartment: (bot: string) => void
  className?: string
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
        <MoveItem bot={bot} department={department} onNewDepartment={onNewDepartment} />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
