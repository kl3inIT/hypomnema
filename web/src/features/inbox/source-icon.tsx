import { BotMessageSquare, Mic, PenLine, Repeat2, type LucideIcon } from "lucide-react"
import type { ComponentType, SVGProps } from "react"

import { GmailIcon, LarkIcon, WhatsAppIcon, ZaloIcon } from "@/components/icons/brand-icons"
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
// channel's mark at 16px, named when pointed at. The label keeps the channel available to screen readers while the
// surrounding row or detail supplies the place.
export function SourceIcon({ source, className }: { source: Source; className?: string }) {
  const Mark = marks[source]

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            aria-label={sourceNames[source]}
            data-source={source}
            className={cn("inline-flex shrink-0", className)}
          />
        }
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
