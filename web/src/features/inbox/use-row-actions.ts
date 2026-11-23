import { toast } from "@/components/ui/toast"
import { errorText } from "@/lib/server-error"

import { snoozeWords, type Action, type Row } from "./inbox-state"
import { useDecideById, useTodoAct, type TodoAct } from "./use-inbox"

// A proposal or an item dropped from the Inbox says so as its reason; the API asks for one.
const dropReason = "Dropped in the Inbox"

// useRowActions runs a row's actions with the same calls as their sources (issue #1093): an Approval's decision, a
// to-do's transitions. A change that succeeded says so in a toast. after is told which row was acted on, so the
// selection can move on (OpenMausBot's InboxPanel auto-advance, InboxPanel.tsx:47-56); open opens a row whose
// action is answered in the detail (a question).
export function useRowActions({
  after,
  open,
}: {
  after: (row: Row) => void
  open: (row: Row) => void
}) {
  const act = useTodoAct()
  const decide = useDecideById()

  const change = (row: Row, todoAct: TodoAct, done: string) => {
    if (row.kind !== "todo") return
    act.mutate(
      { todo: row.todo, change: todoAct },
      {
        onSuccess: () => {
          toast.add({ title: done, type: "success" })
          if (todoAct.act !== "draft") after(row)
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
        if (!approval) return
        decide.mutate(
          { id: approval.id, approve: action === "approve" },
          {
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
        if (row.kind === "todo" && row.todo.draft) copyDraft(row.todo.draft)

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
    }
  }

  return {
    run,
    saveDraft: (row: Row, draft: string, onSaved: () => void) =>
      row.kind === "todo" &&
      act.mutate(
        { todo: row.todo, change: { act: "draft", draft } },
        {
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

// copyDraft puts a draft on the clipboard for the owner to paste where he answers; a Bot never sends it.
export function copyDraft(draft: string) {
  // A browser may refuse the clipboard (no permission, no focus); the toast then says so.
  navigator.clipboard.writeText(draft).then(
    () => toast.add({ title: "Draft copied", type: "success" }),
    () => toast.add({ title: "The browser did not let the draft be copied", type: "error" }),
  )
}
