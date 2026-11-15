import { MoreHorizontal, Pin, PinOff } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { usePinBot } from "@/features/bots/use-bots"
import { cn } from "@/lib/utils"

// PinItem pins or unpins a Bot from its row's menu (issue #470), as OpenMausBot's Bot menu has Pin or Unpin with
// its icon (src/components/SidebarBotMenu.tsx, sidebar.bot.pin and sidebar.bot.unpin; Apache License 2.0).
export function PinItem({ bot, pinned }: { bot: string; pinned: boolean }) {
  const pin = usePinBot()

  return (
    <DropdownMenuItem onClick={() => pin.mutate({ id: bot, pinned: !pinned })}>
      {pinned ? <PinOff /> : <Pin />} {pinned ? "Unpin" : "Pin"}
    </DropdownMenuItem>
  )
}

// BotMenu is the menu of a Bot's row that stands for no one chat, drawn as a chat row's menu (chat-menu.tsx).
export function BotMenu({
  bot,
  name,
  pinned,
  className,
}: {
  bot: string
  name: string
  pinned: boolean
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
        <PinItem bot={bot} pinned={pinned} />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
