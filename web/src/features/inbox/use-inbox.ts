import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useMemo } from "react"

import "@/api/client"
import {
  getInboxNeedsYouOptions,
  getInboxNeedsYouQueryKey,
  getTodoOptions,
  getTodoQueryKey,
  listApprovalsQueryKey,
  listInboxProjectsOptions,
  listInboxProjectsQueryKey,
  listQuestionsOptions,
  listQuestionsQueryKey,
  listTodosOptions,
  listTodosQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import {
  acceptTodo,
  commentTodo,
  createInboxProject,
  decideApproval,
  deleteInboxProject,
  doneTodo,
  dropTodo,
  editInboxProject,
  editTodo,
  handTodo,
  reopenTodo,
  sendTodoDraft,
  snoozeTodo,
} from "@/api/generated/sdk.gen"
import type { Todo } from "@/api/generated/types.gen"
import { usePending } from "@/features/approvals/use-approvals"
import { callsOf } from "@/lib/query-keys"

import { rowsOf } from "./inbox-state"
import type { Write } from "./inbox-thread"

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
  listInboxProjectsQueryKey(),
)

export type TodoAct =
  | { act: "done" }
  | { act: "drop"; reason: string }
  | { act: "snooze"; until?: string }
  | { act: "accept" }
  | { act: "reopen" }
  | { act: "draft"; draft: string }
  // A priority set by the owner has no reason: the one a Bot gave was for the priority it set (issue #1157).
  | { act: "priority"; priority: number }
  // project is the project's id; empty takes the item out of its project.
  | { act: "project"; project: string }
  // hand gives the item to a Bot, the main Bot when none is named, as writing to it does (issue #1143).
  | { act: "hand"; bot?: string }
  // take_back gives the item to the owner, from the person it waited on (issue #1142).
  | { act: "take_back" }

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
        case "priority":
          return (await editTodo({ path, body: { revision, priority: change.priority } })).data
        case "project":
          return (await editTodo({ path, body: { revision, project_id: change.project } })).data
        case "hand":
          return (await handTodo({ path, body: { revision, bot: change.bot || undefined } })).data
        case "take_back":
          return (await editTodo({ path, body: { revision, assignee: { kind: "me" } } })).data
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

// useWriteTodo sends what the owner writes in an item's composer, as writeOf says (issue #1143): a hand-over to a Bot
// with the text as its prompt (POST /inbox/{id}/hand, issue #1111), which gives the item to the Bot and wakes it in
// the item's own chat, no Bot named being the main Bot; a comment (POST /inbox/{id}/comments), which reaches the Bot
// in the item's chat; or, on a closed item, a reopen with the revision read, then the hand-over with the revision
// it made.
export function useWriteTodo() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({
      todo,
      write,
      text,
    }: {
      todo: Todo
      write: Pick<Write, "act" | "bot">
      text: string
    }) => {
      const path = { todo_id: todo.id }
      const bot = write.bot || undefined
      const prompt = text.trim() || undefined

      switch (write.act) {
        case "comment":
          return (await commentTodo({ path, body: { text } })).data
        case "hand":
          return (await handTodo({ path, body: { revision: todo.revision, bot, prompt } })).data
        case "reopen": {
          const open = (await reopenTodo({ path, body: { revision: todo.revision } })).data

          return (await handTodo({ path, body: { revision: open.revision, bot, prompt } })).data
        }
      }
    },
    onSettled: () => void queryClient.invalidateQueries({ predicate: inboxReads }),
  })
}

// useSendDraft sends the text shown, edits included, through the owner's own account to the item's own chat or
// thread (POST /inbox/{id}/send); the server closes the item with the message sent as its evidence.
export function useSendDraft() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ todo, text }: { todo: Todo; text: string }) =>
      (await sendTodoDraft({ path: { todo_id: todo.id }, body: { revision: todo.revision, text } }))
        .data,
    onSettled: () => void queryClient.invalidateQueries({ predicate: inboxReads }),
  })
}

// useProjects reads the Inbox's projects (issue #1157), by name, for the pickers and the filter.
export function useProjects() {
  return useQuery({
    ...listInboxProjectsOptions(),
    select: (p) => p.data,
    refetchInterval: everyMs,
  })
}

export type ProjectWrite =
  | { act: "create"; name: string; color: string }
  | { act: "edit"; id: string; name?: string; color?: string }
  | { act: "delete"; id: string }

// useProjectWrite adds, renames or recolours, and removes a project, as the owner keeps them from the filter's menu;
// a removed project's items are left without one by the server.
export function useProjectWrite() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (write: ProjectWrite) => {
      switch (write.act) {
        case "create":
          return (await createInboxProject({ body: { name: write.name, color: write.color } })).data
        case "edit":
          return (
            await editInboxProject({
              path: { project_id: write.id },
              body: { name: write.name, color: write.color },
            })
          ).data
        case "delete":
          await deleteInboxProject({ path: { project_id: write.id } })

          return undefined
      }
    },
    onSettled: () => void queryClient.invalidateQueries({ predicate: inboxReads }),
  })
}
