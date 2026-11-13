import { createFileRoute } from "@tanstack/react-router"
import { useEffect } from "react"

import { ChatScreen } from "@/features/chat/chat-screen"
import { rememberPlace } from "@/features/shell/last-place"
import { TaskPanel } from "@/features/tasks/task-panel"

type Search = { task?: string }

// A chat keeps the Task a wake-up opened in its URL (issue #303), as the board and the Team page do, so a link or a
// reload opens it again over the chat.
export const Route = createFileRoute("/chats/$chatId")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    task: typeof s.task === "string" && s.task ? s.task : undefined,
  }),
  component: OpenChatRoute,
})

function OpenChatRoute() {
  const { chatId } = Route.useParams()
  useEffect(() => rememberPlace({ kind: "chat", chat: chatId }), [chatId])
  const search = Route.useSearch()
  const navigate = Route.useNavigate()

  return (
    <>
      <ChatScreen chatId={chatId} />
      <TaskPanel
        taskId={search.task}
        onClose={() => void navigate({ search: (prev) => ({ ...prev, task: undefined }) })}
      />
    </>
  )
}
