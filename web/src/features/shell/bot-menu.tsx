import { useQueryClient } from "@tanstack/react-query"
import { MoreHorizontal, Pin, PinOff, Star } from "lucide-react"
import type { ReactNode } from "react"

import "@/api/client"
import { listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { usePinBot, useSetMainChat } from "@/features/bots/use-bots"
import { everyCall } from "@/lib/query-keys"
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
