import { Link } from "@tanstack/react-router"
import { Bot as BotIcon, Layers } from "lucide-react"

import type { SetupChange } from "@/api/generated/types.gen"
import { errorText } from "@/features/agents/profiles-state"
import { cn } from "@/lib/utils"

import { changeLine, useUndoSetupChange, verb, what } from "./setup-changes"

// What the Bot coordinator made, changed or removed of Bots and profiles in a chat (issue #169): each a card where
// it was done, "Made Bot researcher", with Open and Undo. The card is the Routines' "Set up" card
// (features/routines/chat-cards.tsx, adapted from OpenMausBot's TriggerCard), as OpenMausBot leaves a one-line
// receipt with Undo for a team setup change that applied at once (docs/approval-levels.md L111-114).

const action = "rounded px-1 py-0.5 hover:bg-accent hover:text-foreground disabled:opacity-50"

export function SetupChangeCard({ change }: { change: SetupChange }) {
  const undo = useUndoSetupChange()
  const Icon = change.kind === "bot" ? BotIcon : Layers
  const title = `${verb.done[change.action]} ${what(change)}`
  const open = change.action !== "removed" && !(change.action === "made" && change.undone_at)

  return (
    <section
      aria-label={title}
      data-setup-change={change.id}
      className="mb-4 w-full max-w-[640px] rounded-xl border bg-card px-3.5 py-2.5 text-[13px]"
    >
      <div className="flex items-start gap-2">
        <Icon
          aria-hidden
          className={cn(
            "mt-0.5 size-3.5 shrink-0",
            change.undone_at ? "text-muted-foreground" : "text-primary",
          )}
        />
        <p className={cn("min-w-0 leading-snug font-medium", change.undone_at && "line-through")}>
          {title}
        </p>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-5.5 text-xs">
        <span
          className="min-w-0 flex-1 basis-48 truncate text-muted-foreground"
          title={changeLine(change)}
        >
          {changeLine(change)}
        </span>
        <span className="flex shrink-0 items-center gap-0.5 text-muted-foreground">
          {open &&
            (change.kind === "bot" ? (
              <Link to="/bots/$name" params={{ name: change.name }} className={action}>
                Open
              </Link>
            ) : (
              <Link to="/agents/$name" params={{ name: change.name }} className={action}>
                Open
              </Link>
            ))}
          {change.can_undo && (
            <>
              {open && <span aria-hidden>·</span>}
              <button
                type="button"
                disabled={undo.isPending}
                onClick={() => undo.mutate(change)}
                className={cn(action, "text-primary")}
              >
                {undo.isPending ? "Undoing…" : "Undo"}
              </button>
            </>
          )}
        </span>
      </div>
      {undo.error && (
        <p role="alert" className="mt-1 pl-5.5 text-xs text-needs-you">
          {errorText(undo.error)}
        </p>
      )}
    </section>
  )
}
