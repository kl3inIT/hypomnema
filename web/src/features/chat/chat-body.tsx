import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"

import "@/api/client"
import { abortChat, getChat, sendChatMessage, updateChat } from "@/api/generated/sdk.gen"
import type { Chat, ChatAttachment, ChatMessage } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { PendingApprovals } from "@/features/approvals/approval-card"
import { usePending } from "@/features/approvals/use-approvals"
import { firstBot } from "@/features/bots/bots-state"
import { useSetupChanges } from "@/features/bots/setup-changes"
import { useRoutines } from "@/features/routines/use-routines"
import { chatsKey } from "@/features/shell/chats"

import { AttachDrop } from "./attach-drop"
import { useAttachments } from "./attachments"
import { ChatModelControls } from "./chat-model-picker"
import { choiceOf, type ChatChoice } from "./chat-model-state"
import { Composer } from "./composer"
import { ContextMeter } from "./context-meter"
import { Conversation } from "./conversation"
import { DockContext } from "./dock-context"
import { wakeCount } from "./dock-state"
import { DockedQuestions, DockedRuns } from "./docked-work"
import { useDockedWork } from "./use-docked-work"
import { useChatStream } from "./use-chat-stream"
import { OpenFileContext, type OpenFile } from "./open-file-context"

// ChatBody is a chat without its header: the conversation, what waits for the owner, and the composer. It fills
// the page of one chat and a pane of the grid alike. A message sent shows at once, before the stream keeps it, as
// t3code's optimisticUserMessages.ts: it waits under a seq of its own until the server answers with its seq, and
// goes when the kept one arrives; a refused send takes it away again.
//
// Files dropped anywhere on the chat are attached to the message being written (issue #343, AttachDrop);
// onOpenFile opens an attached file in the pane.
export function ChatBody({ chatId, onOpenFile }: { chatId: string; onOpenFile?: OpenFile }) {
  const queryClient = useQueryClient()
  const view = useChatStream(chatId)
  const chat = useQuery({
    queryKey: ["chat", chatId],
    queryFn: async () => (await getChat({ path: { chat_id: chatId } })).data,
  })
  const [sending, setSending] = useState<ChatMessage[]>([])
  const attachments = useAttachments(chatId)
  // What waits above the composer: the Approvals, the questions of this chat's Runs and the Runs themselves.
  const approvals = usePending({ chatId })
  const work = useDockedWork(chatId, wakeCount(view.messages), approvals.data?.length ?? 0)
  const bot = chat.data?.bot ?? firstBot
  // The Routines the Bot set up from this chat have their card where they were set up.
  const routines = useRoutines({ chatId })
  // So do the Bots and profiles the Bot coordinator made, changed or removed here (issue #169).
  const changes = useSetupChanges(chatId)
  const setups = [
    ...(routines.data ?? [])
      .filter((r) => r.created_by === "bot")
      .map((r) => ({ id: r.id, at: r.created_at })),
    ...(changes.data ?? []).map((c) => ({ id: c.id, at: c.created_at, change: c })),
  ]

  // The chat's model and effort (issue #369): shown as chosen at once, kept by the server, and put back as it was
  // when the server refuses them.
  async function choose(next: ChatChoice) {
    const key = ["chat", chatId]
    const before = queryClient.getQueryData<Chat>(key)
    if (before) queryClient.setQueryData<Chat>(key, { ...before, ...next })

    try {
      const { data } = await updateChat({ path: { chat_id: chatId }, body: next })
      queryClient.setQueryData(key, data)
    } catch {
      if (before) queryClient.setQueryData(key, before)
    }
  }

  async function send(text: string, files: ChatAttachment[]) {
    const shown: ChatMessage = {
      seq: -Date.now(),
      chat_id: chatId,
      role: "owner",
      text,
      ...(files.length > 0 && { attachments: files }),
      created_at: new Date().toISOString(),
    }
    setSending((list) => [...list, shown])

    try {
      const { data } = await sendChatMessage({
        path: { chat_id: chatId },
        body: { text, ...(files.length > 0 && { attachments: files }) },
      })
      setSending((list) => list.map((m) => (m === shown ? data : m)))
    } catch (err) {
      setSending((list) => list.filter((m) => m !== shown))
      throw err
    }

    void queryClient.invalidateQueries({ queryKey: chatsKey })
  }

  return (
    <OpenFileContext value={onOpenFile ?? null}>
      <DockContext value={work.dock}>
        <AttachDrop attachments={attachments} className="flex min-h-0 min-w-0 flex-1 flex-col">
          {chat.data && !chat.data.coordinator && (
            <output className="block px-5 pb-2 text-sm text-muted-foreground">
              No coordinator runs on the server: messages are kept and nothing answers.
            </output>
          )}
          {changes.error && (
            <output className="mx-auto flex w-full max-w-(--chat-content-max-width) items-center gap-2 px-5 pb-2 text-sm text-muted-foreground">
              The changes the Bot made here could not be read.
              <Button variant="outline" size="xs" onClick={() => void changes.refetch()}>
                Retry
              </Button>
            </output>
          )}
          <Conversation
            view={view}
            local={{ sending }}
            bot={bot}
            setups={setups}
            empty={
              <p className="text-center text-sm text-muted-foreground">Send a message to start.</p>
            }
          />
          <div className="px-3 pb-[calc(env(safe-area-inset-bottom)+1rem)] sm:px-12 sm:pb-[calc(env(safe-area-inset-bottom)+1.25rem)]">
            {/* The stream is down and being opened again: a quiet row, as Omnigent's "Connecting…"
            (web/src/pages/ChatIndicators.tsx, ConnectionIndicator). */}
            {view.reconnecting && (
              <output className="mx-auto mb-2 flex w-full max-w-(--chat-content-max-width) items-center justify-center gap-2 text-sm text-muted-foreground">
                <Spinner aria-hidden="true" />
                Reconnecting…
              </output>
            )}
            {/* What pi asked to do with an account and what this chat's workers ask the owner, both answered here,
            then this chat's Runs as strips right above the composer, as Orca docks its task list (issue #314). The
            cards scroll in their own box, so that the message box stays on the screen under them. */}
            <div className="mx-auto mb-3 flex max-h-[50dvh] w-full max-w-(--chat-content-max-width) flex-col gap-3 overflow-y-auto empty:hidden">
              <PendingApprovals chatId={chatId} />
              <DockedQuestions work={work} />
              <DockedRuns chatId={chatId} work={work} />
            </div>
            <Composer
              working={view.working}
              bot={bot}
              onSend={send}
              onStop={() => void abortChat({ path: { chat_id: chatId } })}
              attachments={attachments}
              controls={
                chat.data && (
                  <>
                    <ContextMeter
                      chatId={chatId}
                      usage={chat.data.context_usage}
                      working={view.working}
                    />
                    <ChatModelControls
                      bot={bot}
                      choice={choiceOf(chat.data)}
                      onChange={(next) => void choose(next)}
                    />
                  </>
                )
              }
            />
          </div>
        </AttachDrop>
      </DockContext>
    </OpenFileContext>
  )
}
