// The Inbox's command menu is adapted from circle (https://github.com/ln-dev7/circle, commit b0ba7dd):
// components/layout/command-palette.tsx, the ⌘K/Ctrl+K palette aware of the item in context: a chip naming it that ⌫
// clears, the item's commands with their pages (Set priority…, Move to project…), Escape or Backspace back to the first page,
// and with no item, the list to jump to; adapted to the app's CommandDialog (Base UI) and to the Inbox's actions
// (issue #1157): priority, project, give to the main Bot, snooze, done and drop.
//
// MIT License
//
// Copyright (c) 2025 lndev-ui | Circle Template
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
// documentation files (the "Software"), to deal in the Software without restriction, including without limitation the
// rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all copies or substantial portions of
// the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO
// THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
// TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

import { AlarmClock, Bot, Box, Check, FolderIcon, Layers, Trash2, Undo2 } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

import type { InboxProject } from "@/api/generated/types.gen"
import {
  CommandDialog,
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@/components/ui/command"
import { priorities } from "@/components/ui/priority"
import { ProjectDot } from "@/components/ui/project-selector"

import { KindIcon } from "./inbox-row"
import {
  detailActions,
  isClosed,
  snoozePresets,
  titleOf,
  type Action,
  type Row,
} from "./inbox-state"

type Route = "root" | "priority" | "project" | "snooze"

// What the menu does with the item in context; the screen runs each the way its row and detail do.
export type MenuCommands = {
  priority: (row: Row, priority: number) => void
  project: (row: Row, project: string, name: string) => void
  hand: (row: Row) => void
  action: (row: Row, action: Action, until?: number) => void
  jump: (key: string) => void
}

// CommandMenu is the Inbox's ⌘K/Ctrl+K menu. On the selected to-do it changes the priority, the project, gives it to
// the main Bot (handName names it), snoozes it, marks it done or drops it; with none selected, or once its chip is
// cleared, it searches the items by title and opens the one picked.
export function CommandMenu({
  item,
  rows,
  projects,
  handName,
  now,
  commands,
}: {
  item: Row | undefined
  rows: Row[]
  projects: InboxProject[]
  handName: string
  now: number
  commands: MenuCommands
}) {
  const [open, setOpen] = useState(false)
  const [route, setRoute] = useState<Route>("root")
  const [query, setQuery] = useState("")
  // When true, the item's chip was dismissed with ⌫, as circle's contextCleared.
  const [cleared, setCleared] = useState(false)
  const todo = !cleared && item?.kind === "todo" ? item : undefined
  // The input takes the keys as soon as the menu or one of its pages opens, as circle's autoFocus does; the dialog's
  // own first focus may land on the popup, and a j or k typed there would move the list behind it.
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (!open) return
    const frame = requestAnimationFrame(() => input.current?.focus())

    return () => cancelAnimationFrame(frame)
  }, [open, route, cleared])

  const reset = useCallback(() => {
    setRoute("root")
    setQuery("")
    setCleared(false)
  }, [])

  const close = () => {
    setOpen(false)
    reset()
  }

  // ⌘K / Ctrl+K, as circle's palette listens on the window.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault()
        setOpen((value) => {
          if (value) reset()

          return !value
        })
      }
    }
    window.addEventListener("keydown", onKeyDown)

    return () => window.removeEventListener("keydown", onKeyDown)
  }, [reset])

  const go = (next: Route) => {
    setRoute(next)
    setQuery("")
  }
  const run = (fn: () => void) => {
    fn()
    close()
  }

  const offered = todo ? detailActions(todo, now) : []
  const editable = !!todo && !isClosed(todo.todo)
  const handable = editable && todo.todo.assignee.kind !== "bot"

  return (
    <CommandDialog
      open={open}
      onOpenChange={(value) => {
        setOpen(value)
        if (!value) reset()
      }}
      title="Command menu"
      description="Type a command or search"
    >
      <Command>
        {todo && (
          <div className="flex items-center gap-1.5 px-2 pt-2 pb-1">
            <span
              data-testid="command-context"
              className="inline-flex max-w-full items-center gap-1.5 rounded-md border border-border/60 bg-muted/70 px-2 py-1 text-xs"
            >
              <KindIcon row={todo} now={now} className="[&>svg]:size-3.5" />
              <span className="truncate">{todo.todo.title}</span>
              <button
                type="button"
                tabIndex={-1}
                // The input keeps the keys: the chip's button takes no focus when pressed.
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => setCleared(true)}
                className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
                aria-label="Clear the item"
              >
                ⌫
              </button>
            </span>
          </div>
        )}
        <CommandInput
          ref={input}
          placeholder={todo ? "Type a command…" : "Search the Inbox…"}
          value={query}
          onValueChange={setQuery}
          onKeyDown={(event) => {
            if (event.key === "Escape" && route !== "root") {
              event.preventDefault()
              event.stopPropagation()
              go("root")
            }
            if (event.key === "Backspace" && query === "" && route !== "root") go("root")
          }}
        />
        <CommandList>
          <CommandEmpty>No results found.</CommandEmpty>

          {route === "root" && todo && (
            <CommandGroup heading="Item">
              {editable && (
                <CommandItem value="Set priority" onSelect={() => go("priority")}>
                  <Layers className="text-muted-foreground" />
                  Set priority…
                </CommandItem>
              )}
              {editable && (
                <CommandItem value="Move to project" onSelect={() => go("project")}>
                  <Box className="text-muted-foreground" />
                  Move to project…
                </CommandItem>
              )}
              {handable && (
                <CommandItem
                  value={`Give to ${handName}`}
                  onSelect={() => run(() => commands.hand(todo))}
                >
                  <Bot className="text-muted-foreground" />
                  Give to {handName}
                </CommandItem>
              )}
              {offered.includes("take_back") && (
                <CommandItem
                  value="Take it back"
                  onSelect={() => run(() => commands.action(todo, "take_back"))}
                >
                  <Undo2 className="text-muted-foreground" />
                  Take it back
                </CommandItem>
              )}
              {offered.includes("snooze") && (
                <CommandItem value="Snooze" onSelect={() => go("snooze")}>
                  <AlarmClock className="text-muted-foreground" />
                  Snooze…
                </CommandItem>
              )}
              {offered.includes("done") && (
                <CommandItem
                  value="Mark done"
                  onSelect={() => run(() => commands.action(todo, "done"))}
                >
                  <Check className="text-muted-foreground" />
                  Mark done
                  <CommandShortcut>E</CommandShortcut>
                </CommandItem>
              )}
              {offered.includes("drop") && (
                <CommandItem value="Drop" onSelect={() => run(() => commands.action(todo, "drop"))}>
                  <Trash2 className="text-muted-foreground" />
                  Drop
                </CommandItem>
              )}
            </CommandGroup>
          )}

          {route === "root" && !todo && (
            <CommandGroup heading="Go to">
              {rows.map((row) => (
                <CommandItem
                  key={row.key}
                  value={`${titleOf(row)} ${row.key}`}
                  onSelect={() => run(() => commands.jump(row.key))}
                >
                  <KindIcon row={row} now={now} />
                  <span className="min-w-0 truncate">{titleOf(row)}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          )}

          {route === "priority" && todo && (
            <CommandGroup heading="Set priority…">
              {priorities.map((p) => (
                <CommandItem
                  key={p.value}
                  value={p.name}
                  data-checked={todo.todo.priority === p.value}
                  onSelect={() => run(() => commands.priority(todo, p.value))}
                >
                  <p.icon className="text-muted-foreground" />
                  {p.name}
                </CommandItem>
              ))}
            </CommandGroup>
          )}

          {route === "project" && todo && (
            <CommandGroup heading="Move to project…">
              <CommandItem
                value="No project"
                data-checked={!todo.todo.project_id}
                onSelect={() => run(() => commands.project(todo, "", ""))}
              >
                <FolderIcon className="text-muted-foreground" />
                No project
              </CommandItem>
              {projects.map((p) => (
                <CommandItem
                  key={p.id}
                  value={`${p.name} ${p.id}`}
                  data-checked={todo.todo.project_id === p.id}
                  onSelect={() => run(() => commands.project(todo, p.id, p.name))}
                >
                  <ProjectDot color={p.color} />
                  {p.name}
                </CommandItem>
              ))}
            </CommandGroup>
          )}

          {route === "snooze" && todo && (
            <CommandGroup heading="Snooze…">
              {snoozePresets(now).map((p) => (
                <CommandItem
                  key={p.id}
                  value={p.name}
                  onSelect={() => run(() => commands.action(todo, "snooze", p.at))}
                >
                  <AlarmClock className="text-muted-foreground" />
                  {p.name}
                </CommandItem>
              ))}
            </CommandGroup>
          )}
        </CommandList>
      </Command>
    </CommandDialog>
  )
}
