import { CircleCheck, ChevronRight, Inbox, Search } from "lucide-react"
import { useEffect, useMemo, useRef, useState } from "react"

import { ErrorAlert } from "@/components/error-alert"
import { PageHeader, PageHeading } from "@/components/page-header"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Kbd } from "@/components/ui/kbd"
import { Spinner } from "@/components/ui/spinner"
import { botNameOf } from "@/features/bots/bots-state"
import { useBots } from "@/features/bots/use-bots"
import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"

import { InboxDetail } from "./inbox-detail"
import { FilterChips, InboxFilterButton } from "./inbox-filter"
import { InboxRow } from "./inbox-row"
import { useSeen } from "./inbox-seen"
import {
  actionsOf,
  firstWaiting,
  fold,
  foldedByDefault,
  fromText,
  groupIds,
  groupNames,
  groupOf,
  groupRows,
  matches,
  nextAfter,
  noFilter,
  sourceOf,
  todayCounts,
  type Filter,
  type GroupId,
  type Row,
} from "./inbox-state"
import { useInbox } from "./use-inbox"
import { useKeptChats, type KeptChats } from "./use-origin"
import { useRowActions } from "./use-row-actions"

// tick is how often the page's own clock moves, for due dates, snoozes coming back and "5m ago".
const tick = 30_000

function useClock() {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), tick)

    return () => window.clearInterval(timer)
  }, [])

  return now
}

// editable says a key goes to a field, a menu or a dialog, not to the list's keys.
function editable(target: EventTarget | null) {
  return (
    target instanceof HTMLElement &&
    (target.isContentEditable ||
      ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) ||
      !!target.closest("[role=dialog],[role=menu],[role=alertdialog]"))
  )
}

// chatOf is the kept chat's name a row's Zalo or WhatsApp source is in, for the search.
function chatOf(row: Row, chats: KeptChats) {
  if (row.kind !== "todo" || !row.todo.source.id) return undefined

  return chats.get(`${sourceOf(row)}:${row.todo.source.id}`)?.name
}

// InboxScreen is the Inbox (issue #1093): on a computer the list on the left and the selected item on the right, as
// Chatwoot's conversation list beside the open conversation (ConversationView.vue), the first item of Needs you open
// when none is chosen (issue #1110); on a phone one pane at a time, the item full screen with a way back. The list is in groups, Needs you first, each folding, after Orca's Items
// tab. j and k move, e marks done and c copies the draft, after Orca's item chords (use-bot-item-chords.ts: step
// to the previous or next item, never from a field); Escape goes back to the list on a phone.
export function InboxScreen({
  selected: chosen,
  onSelect,
}: {
  selected?: string
  onSelect: (key: string | undefined) => void
}) {
  const now = useClock()
  const phone = useIsMobile()
  const { rows, loading, error } = useInbox(now)
  const bots = useBots()
  const botName = (id: string) => botNameOf(bots.data, id)
  const chats = useKeptChats()
  const { unread, markSeen } = useSeen()
  const [filter, setFilter] = useState<Filter>(noFilter)
  const [open, setOpen] = useState<Record<GroupId, boolean>>(
    () =>
      Object.fromEntries(groupIds.map((g) => [g, !foldedByDefault.has(g)])) as Record<
        GroupId,
        boolean
      >,
  )
  const [more, setMore] = useState<Partial<Record<GroupId, boolean>>>({})

  const shown = useMemo(
    () =>
      rows.filter((r) =>
        matches(
          r,
          filter,
          fromText(r, { chat: chatOf(r, chats) }, (id) => botNameOf(bots.data, id)),
        ),
      ),
    [rows, filter, bots.data, chats],
  )
  const groups = useMemo(() => groupRows(shown, now), [shown, now])
  // On a computer, the first item of Needs you is open when none is chosen, as OpenMausBot's InboxPanel keeps an item
  // open (InboxPanel.tsx:47-56); a phone shows the list first.
  const selected = chosen ?? (phone ? undefined : firstWaiting(groups))
  const folds = useMemo(
    () =>
      Object.fromEntries(
        groupIds.map((g) => [g, fold(groups[g], { open: !!more[g], selected })]),
      ) as Record<GroupId, ReturnType<typeof fold>>,
    [groups, more, selected],
  )
  // order is every row on screen, top to bottom: what j and k step through and where the selection moves on to.
  const order = groupIds.flatMap((g) => (open[g] ? folds[g].visible.map((r) => r.key) : []))
  const current = rows.find((r) => r.key === selected)
  const group = current ? groupOf(current, now) : undefined
  const at = current && group ? groups[group].findIndex((r) => r.key === current.key) : -1
  const position =
    current && group && at >= 0 ? { index: at, count: groups[group].length, group } : null

  useEffect(() => {
    if (current) markSeen(current)
  }, [current, markSeen])

  const select = (key: string | undefined) => onSelect(key)

  // An open item that leaves the list, an Approval or a question answered here or at its source, gives way to the
  // next one in the list as it was, as OpenMausBot's InboxPanel moves on from a line once it is answered
  // (InboxPanel.tsx:47-56, its order ref).
  const was = useRef<string[]>([])
  useEffect(() => {
    if (selected && order.includes(selected)) {
      was.current = order
    } else if (selected && !loading && !current && was.current.includes(selected)) {
      const left = was.current.filter((k) => k === selected || order.includes(k))
      was.current = []
      onSelect(nextAfter(left, selected))
    }
  })
  const actions = useRowActions({
    // After an action on the open item, the next one opens, from the list as it was when the action began.
    after: (row) => {
      if (row.key === selected) select(nextAfter(order, row.key))
    },
    open: (row) => select(row.key),
  })

  const step = (by: -1 | 1) => {
    if (!current || !group) {
      select(order[0])

      return
    }
    const list = groups[group]
    const next = list[at + by]
    if (next) {
      select(next.key)

      return
    }
    // Past a group's end, the next group's first row on screen.
    const i = order.indexOf(current.key)
    const beyond = i >= 0 ? order[i + by] : undefined
    if (beyond) select(beyond)
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || editable(e.target)) return

      switch (e.key) {
        case "j":
          e.preventDefault()
          step(1)
          break
        case "k":
          e.preventDefault()
          step(-1)
          break
        case "e":
          if (current && actionsOf(current, now).includes("done")) {
            e.preventDefault()
            actions.run(current, "done")
          }
          break
        case "c":
          if (current?.kind === "todo" && current.todo.draft) {
            e.preventDefault()
            actions.run(current, "copy")
          }
          break
        case "Escape":
          if (phone && current) {
            e.preventDefault()
            select(undefined)
          }
          break
      }
    }
    window.addEventListener("keydown", onKey)

    return () => window.removeEventListener("keydown", onKey)
  })

  const list = (
    <section
      aria-label="Inbox list"
      className={cn(
        "flex min-h-0 min-w-0 flex-col md:w-[520px] md:shrink-0 md:border-r",
        phone && current ? "hidden" : "flex-1 md:flex-none",
      )}
    >
      <PageHeader />
      <div className="flex flex-col gap-3 px-3 pt-1 pb-3 sm:px-5">
        <PageHeading title="Inbox" className="pb-3 md:pt-4">
          <div className="flex items-center gap-2">
            <div className="w-52 max-md:flex-1">
              <InputGroup>
                <InputGroupAddon>
                  <Search />
                </InputGroupAddon>
                <InputGroupInput
                  type="search"
                  aria-label="Search the Inbox"
                  placeholder="Search…"
                  value={filter.query}
                  onChange={(e) => setFilter({ ...filter, query: e.target.value })}
                />
              </InputGroup>
            </div>
            <InboxFilterButton filter={filter} onFilter={setFilter} />
          </div>
        </PageHeading>
        <FilterChips filter={filter} onFilter={setFilter} />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-8 sm:px-3">
        {error ? (
          <div className="px-2">
            <ErrorAlert error={error} />
          </div>
        ) : loading ? (
          <div className="flex justify-center py-10">
            <Spinner />
          </div>
        ) : !rows.length ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Inbox />
              </EmptyMedia>
              <EmptyTitle>Nothing in the Inbox</EmptyTitle>
              <EmptyDescription>
                What a Bot finds in your messages, what it asks you and what you write down shows
                here.
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : !shown.length ? (
          <p className="px-3 py-8 text-center text-sm text-muted-foreground">Nothing matches.</p>
        ) : (
          groupIds.map((g) =>
            groups[g].length ? (
              <section key={g} aria-label={groupNames[g]} className="mb-2">
                <button
                  type="button"
                  aria-expanded={open[g]}
                  onClick={() => setOpen({ ...open, [g]: !open[g] })}
                  className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-xs font-medium text-muted-foreground hover:text-foreground"
                >
                  <ChevronRight
                    className={cn("size-3.5 transition-transform", open[g] && "rotate-90")}
                    aria-hidden
                  />
                  <span className={cn(g === "needs_you" && "text-needs-you")}>{groupNames[g]}</span>
                  <span className="tabular-nums">{groups[g].length}</span>
                </button>
                {open[g] && (
                  <>
                    <ul className="flex flex-col gap-px">
                      {folds[g].visible.map((row: Row) => (
                        <InboxRow
                          key={row.key}
                          row={row}
                          chats={chats}
                          botName={botName}
                          now={now}
                          selected={row.key === selected}
                          unread={unread(row)}
                          onOpen={() => select(row.key)}
                          onAction={(action, until) => actions.run(row, action, until)}
                        />
                      ))}
                    </ul>
                    {folds[g].folds && (
                      <button
                        type="button"
                        onClick={() => setMore({ ...more, [g]: !more[g] })}
                        className="px-2 py-1.5 text-xs text-link hover:underline"
                      >
                        {more[g] ? "Show less" : `Show ${folds[g].hidden} more`}
                      </button>
                    )}
                  </>
                )}
              </section>
            ) : null,
          )
        )}
      </div>
    </section>
  )

  return (
    <main className="flex min-w-0 flex-1">
      {list}
      {current ? (
        <InboxDetail
          key={current.key}
          row={current}
          now={now}
          position={position}
          phone={phone}
          pending={actions.pending}
          botName={botName}
          chats={chats}
          onBack={() => select(undefined)}
          onStep={step}
          onAction={(action, until) => actions.run(current, action, until)}
          onAnswered={() => {
            if (current.key === selected) select(nextAfter(order, current.key))
          }}
          onSaveDraft={(draft, onSaved) => actions.saveDraft(current, draft, onSaved)}
        />
      ) : (
        !phone && <NothingWaits rows={rows} now={now} loading={loading} />
      )}
    </main>
  )
}

// NothingWaits is the right pane when nothing waits on the owner: it says so, with what the Inbox did today, and the
// keys that move through the list.
function NothingWaits({ rows, now, loading }: { rows: Row[]; now: number; loading: boolean }) {
  if (loading) return <div className="flex-1" />

  const today = todayCounts(rows, now)

  return (
    <div className="flex min-w-0 flex-1 items-center justify-center p-6">
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <CircleCheck />
          </EmptyMedia>
          <EmptyTitle>Nothing waits on you</EmptyTitle>
          <EmptyDescription>
            Today: {today.added} added, {today.closed} closed.
          </EmptyDescription>
          <EmptyDescription>
            <Kbd>j</Kbd> and <Kbd>k</Kbd> move, <Kbd>e</Kbd> marks done, <Kbd>c</Kbd> copies the
            draft.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    </div>
  )
}
