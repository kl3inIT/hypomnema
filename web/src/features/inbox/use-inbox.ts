import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useMemo } from "react"

import "@/api/client"
import {
  getInboxNeedsYouOptions,
  getInboxNeedsYouQueryKey,
  getTodoOptions,
  getTodoQueryKey,
  listApprovalsQueryKey,
  listQuestionsOptions,
  listQuestionsQueryKey,
  listTodosOptions,
  listTodosQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import {
  acceptTodo,
  decideApproval,
  doneTodo,
  dropTodo,
  editTodo,
  reopenTodo,
  snoozeTodo,
} from "@/api/generated/sdk.gen"
import type { Todo } from "@/api/generated/types.gen"
import { usePending } from "@/features/approvals/use-approvals"
import { callsOf } from "@/lib/query-keys"

import { rowsOf } from "./inbox-state"

// The Inbox reads the to-dos (GET /inbox), the ones the server counts as needing the owner, the latest closed, and
// the asks from their own lists (issue #1093, section 6). A to-do changes without an event on the stream, so the
// lists are read again every few seconds while the page is open, as the Approvals are (use-approvals.ts).
const everyMs = 10_000
const page = 200

export function useInbox(now: number) {
  const todos = useQuery({
    ...listTodosOptions({ query: { include_snoozed: true, limit: page } }),
    select: (p) => p.data,
    refetchInterval: everyMs,
  })
  const needsYou = useQuery({
    ...listTodosOptions({ query: { needs_you: true, limit: page } }),
    select: (p) => p.data.map((t) => t.id),
    refetchInterval: everyMs,
  })
  const closed = useQuery({
    ...listTodosOptions({ query: { state: ["done", "dropped", "duplicate"], limit: page } }),
    select: (p) => p.data,
    refetchInterval: everyMs,
  })
  const approvals = usePending()
  const questions = useQuery({
    ...listQuestionsOptions({ query: { status: "open", addressed_to: "owner" } }),
    select: (p) => p.data,
    refetchInterval: everyMs,
  })

  const rows = useMemo(
    () =>
      rowsOf(
        {
          todos: todos.data,
          needsYou: needsYou.data,
          closedTodos: closed.data,
          approvals: approvals.data,
          questions: questions.data,
        },
        now,
      ),
    [todos.data, needsYou.data, closed.data, approvals.data, questions.data, now],
  )

  return {
    rows,
    loading: todos.isPending || needsYou.isPending,
    error: todos.error ?? needsYou.error ?? closed.error,
  }
}

// useInboxCount is the sidebar's count: what Needs you holds, and the proposals not yet triaged.
export function useInboxCount() {
  return useQuery({
    ...getInboxNeedsYouOptions(),
    select: (n) => n.count,
    refetchInterval: everyMs,
  })
}

// useTodo reads one to-do with its history, for the detail.
export function useTodo(id: string | undefined) {
  return useQuery({
    ...getTodoOptions({ path: { todo_id: id ?? "" } }),
    enabled: !!id,
    refetchInterval: everyMs,
  })
}

// inboxReads matches every read an action may change: the to-dos, the count, the Approvals and the questions.
export const inboxReads = callsOf(
  listTodosQueryKey(),
  getInboxNeedsYouQueryKey(),
  getTodoQueryKey({ path: { todo_id: "" } }),
  listApprovalsQueryKey(),
  listQuestionsQueryKey(),
)

export type TodoAct =
  | { act: "done" }
  | { act: "drop"; reason: string }
  | { act: "snooze"; until?: string }
  | { act: "accept" }
  | { act: "reopen" }
  | { act: "draft"; draft: string }

// useTodoAct changes a to-do with the call its button names, carrying the revision read; a stale one is the
// server's conflict, shown as it says it.
export function useTodoAct() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ todo, change }: { todo: Todo; change: TodoAct }) => {
      const path = { todo_id: todo.id }
      const revision = todo.revision

      switch (change.act) {
        case "done":
          return (await doneTodo({ path, body: { revision } })).data
        case "drop":
          return (await dropTodo({ path, body: { revision, reason: change.reason } })).data
        case "snooze":
          return (await snoozeTodo({ path, body: { revision, until: change.until } })).data
        case "accept":
          return (await acceptTodo({ path, body: { revision } })).data
        case "reopen":
          return (await reopenTodo({ path, body: { revision } })).data
        case "draft":
          return (await editTodo({ path, body: { revision, draft: change.draft } })).data
      }
    },
    onSettled: () => void queryClient.invalidateQueries({ predicate: inboxReads }),
  })
}

// useDecideById answers an Approval from its row, the same call as its card in the chat (POST
// /approvals/{id}/decision).
export function useDecideById() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ id, approve }: { id: string; approve: boolean }) =>
      (
        await decideApproval({
          path: { approval_id: id },
          body: { decision: approve ? "approve" : "reject" },
        })
      ).data,
    onSettled: () => void queryClient.invalidateQueries({ predicate: inboxReads }),
  })
}
