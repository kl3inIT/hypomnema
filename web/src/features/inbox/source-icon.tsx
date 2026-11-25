import { BotMessageSquare, Mic, PenLine, Repeat2, type LucideIcon } from "lucide-react"
import type { ComponentType, SVGProps } from "react"

import { AgentAvatar } from "@/components/agent-avatar"
import { GmailIcon, LarkIcon, WhatsAppIcon, ZaloIcon } from "@/components/icons/brand-icons"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

import { sourceNames, type Source } from "./inbox-state"

// Each source's mark: a messaging or mail service's own logo (docs/conventions.md, "a thing that has a logo is shown
// with it"), the app's own icon for its Meetings, Routines and chats, as the sidebar shows them, and a pen for what
// the owner wrote down himself.
const marks: Record<Source, ComponentType<SVGProps<SVGSVGElement>> | LucideIcon> = {
  zalo: ZaloIcon,
  whatsapp: WhatsAppIcon,
  lark: LarkIcon,
  gmail: GmailIcon,
  meeting: Mic,
  chat: BotMessageSquare,
  hand: PenLine,
  routine: Repeat2,
}

const brands = new Set<Source>(["zalo", "whatsapp", "lark", "gmail"])

// SourceIcon is where an item came from, as Chatwoot's ChannelIcon beside an inbox's name (InboxName.vue:14): the
// channel's mark at 16px, named when pointed at. A screen reader hears the row's title and the detail's path, which
// names the channel in words, so the mark itself is hidden from it.
export function SourceIcon({ source, className }: { source: Source; className?: string }) {
  const Mark = marks[source]

  return (
    <Tooltip>
      <TooltipTrigger
        render={<span data-source={source} className={cn("inline-flex shrink-0", className)} />}
      >
        <Mark
          aria-hidden
          className={cn("size-4", !brands.has(source) && "text-muted-foreground")}
        />
      </TooltipTrigger>
      <TooltipContent>{sourceNames[source]}</TooltipContent>
    </Tooltip>
  )
}

// SourceAvatar is a row's avatar, the platform it came from (owner, 2026-11-25, issue #1140): Chatwoot's card puts
// its contact's Avatar there (ConversationCard/CardAvatar.vue), but the Inbox has no pictures of people, so the
// round tile holds the channel's mark instead, the Bot's face for what came from a Bot's chat, and a pen for what
// the owner wrote down himself. The tile is shadcn's Avatar, neutral; the platform is named when pointed at and to
// a screen reader.
export function SourceAvatar({
  source,
  bot,
  className,
}: {
  source: Source
  bot?: string
  className?: string
}) {
  const Mark = marks[source]
  const name = sourceNames[source]

  return (
    <Tooltip>
      <TooltipTrigger
        render={<span data-source={source} className={cn("inline-flex shrink-0", className)} />}
      >
        <Avatar>
          <AvatarFallback>
            {bot ? (
              <AgentAvatar name={bot} className="size-6" />
            ) : (
              <Mark
                aria-hidden
                className={cn("size-4", !brands.has(source) && "text-muted-foreground")}
              />
            )}
          </AvatarFallback>
        </Avatar>
        <span className="sr-only">{name}</span>
      </TooltipTrigger>
      <TooltipContent>{name}</TooltipContent>
    </Tooltip>
  )
}
