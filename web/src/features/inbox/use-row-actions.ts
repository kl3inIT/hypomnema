import { useRef } from "react"

import { toast } from "@/components/ui/toast"
import { errorText } from "@/lib/server-error"

import { priorityOf } from "@/components/ui/priority"

import { snoozeWords, type Action, type Row } from "./inbox-state"
import { useDecideById, useTodoAct, type TodoAct } from "./use-inbox"

// A proposal or an item dropped from the Inbox says so as its reason; the API asks for one.
const dropReason = "Dropped in the Inbox"

// useRowActions runs a row's actions with the same calls as their sources (issue #1093): an Approval's decision, a
// to-do's transitions. A change that succeeded says so in a toast. after is told which row was acted on, so the
// selection can move on (OpenMausBot's InboxPanel auto-advance, InboxPanel.tsx:47-56); open opens a row whose
// action is answered in the detail (a question). One change runs at a time, whether it came from the e key, a row's
// buttons or the detail's: a second press while the first is in flight would send the same revision again and be
// refused (issue #1097; the detail's own guard, inbox-detail.tsx, made shared).
export function useRowActions({
  after,
  open,
}: {
  after: (row: Row) => void
  open: (row: Row) => void
}) {
  const act = useTodoAct()
  const decide = useDecideById()
  // busy is set from the press to the answer: the mutations' isPending shows only after the next render, and the
  // e key can be pressed twice before it.
  const busy = useRef(false)
  const start = () => {
    if (busy.current) return false
    busy.current = true

    return true
  }
  const settled = () => {
    busy.current = false
  }

  const change = (row: Row, todoAct: TodoAct, done: string, moveOn = todoAct.act !== "draft") => {
    if (row.kind !== "todo" || !start()) return
    act.mutate(
      { todo: row.todo, change: todoAct },
      {
        onSettled: settled,
        onSuccess: () => {
          toast.add({ title: done, type: "success" })
          if (moveOn) after(row)
        },
        onError: (error) =>
          toast.add({ title: "Not changed", description: errorText(error), type: "error" }),
      },
    )
  }

  const run = (row: Row, action: Action, until?: number) => {
    switch (action) {
      case "approve":
      case "decline": {
        const approval =
          row.kind === "approval" ? row.approval : row.kind === "todo" ? row.approval : undefined
        if (!approval || !start()) return
        decide.mutate(
          { id: approval.id, approve: action === "approve" },
          {
            onSettled: settled,
            onSuccess: () => {
              toast.add({ title: action === "approve" ? "Approved" : "Declined", type: "success" })
              after(row)
            },
            onError: (error) =>
              toast.add({ title: "Not answered", description: errorText(error), type: "error" }),
          },
        )

        return
      }
      case "answer":
        open(row)

        return
      case "copy":
        if (row.kind === "todo" && row.todo.draft) void copyDraft(row.todo.draft)

        return
      case "done":
        change(row, { act: "done" }, "Done")

        return
      case "snooze":
        if (until)
          change(row, { act: "snooze", until: new Date(until).toISOString() }, snoozed(until))

        return
      case "unsnooze":
        change(row, { act: "snooze" }, "Back in the Inbox")

        return
      case "accept":
        change(row, { act: "accept" }, "Accepted")

        return
      case "drop":
        change(row, { act: "drop", reason: dropReason }, "Dropped")

        return
      case "reopen":
        change(row, { act: "reopen" }, "Reopened")

        return
      case "take_back":
        // The item stays selected, now in Open, his again.
        change(row, { act: "take_back" }, "Taken back: it is yours again", false)
    }
  }

  return {
    run,
    // setPriority, setProject and hand are the changes the priority icon, the department picker and Cmd+K make
    // (issue #1157); they leave the selection where it is.
    setPriority: (row: Row, priority: number) =>
      change(
        row,
        { act: "priority", priority },
        `Priority set to ${priorityName(priority)}`,
        false,
      ),
    setProject: (row: Row, project: string, name: string) =>
      change(
        row,
        { act: "project", project },
        project ? `Moved to ${name}` : "Removed from its department",
        false,
      ),
    hand: (row: Row, bot: string, name: string) =>
      change(row, { act: "hand", bot }, `Given to ${name}`, false),
    saveDraft: (row: Row, draft: string, onSaved: () => void) =>
      row.kind === "todo" &&
      start() &&
      act.mutate(
        { todo: row.todo, change: { act: "draft", draft } },
        {
          onSettled: settled,
          onSuccess: () => {
            toast.add({ title: "Draft saved", type: "success" })
            onSaved()
          },
          onError: (error) =>
            toast.add({ title: "Not saved", description: errorText(error), type: "error" }),
        },
      ),
    pending: act.isPending || decide.isPending,
  }
}

function snoozed(until: number) {
  return `Snoozed until ${snoozeWords(until, Date.now())}`
}

// copyDraft puts a draft on the clipboard for the owner to paste where he answers; a Bot never sends it. It says
// whether it was copied.
export function copyDraft(draft: string): Promise<boolean> {
  // A browser may refuse the clipboard (no permission, no focus); the toast then says so.
  return navigator.clipboard.writeText(draft).then(
    () => {
      toast.add({ title: "Draft copied", type: "success" })

      return true
    },
    () => {
      toast.add({ title: "The browser did not let the draft be copied", type: "error" })

      return false
    },
  )
}

function priorityName(priority: number) {
  return priorityOf(priority).name.toLowerCase()
}
