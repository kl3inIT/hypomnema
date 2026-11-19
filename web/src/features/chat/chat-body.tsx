import { useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { useState } from "react"

import "@/api/client"
import { getChatQueryKey, listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import {
  abortChat,
  createChat,
  removeQueuedChatMessage,
  sendChatMessage,
  sendQueuedChatMessage,
  updateChat,
} from "@/api/generated/sdk.gen"
import type {
  Chat,
  ChatAttachment,
  ChatMessage,
  QueuedChatMessage,
} from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { PendingApprovals } from "@/features/approvals/approval-card"
import { usePending } from "@/features/approvals/use-approvals"
import { useBotName, useMainBot, useRefreshBots } from "@/features/bots/use-bots"
import { useSetupChanges } from "@/features/bots/setup-changes"
import { useRoutines } from "@/features/routines/use-routines"
import { OpenArtifactContext, type OpenArtifact } from "@/features/artifacts/open-artifact-context"
import { OpenBrowserTabContext } from "@/features/browser/open-tab-context"
import { OpenTakeContext } from "@/features/takes/open-take-context"
import { useChat } from "@/features/shell/chats"
import { everyCall } from "@/lib/query-keys"

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
import { chatDraft, readDraft } from "./drafts"
import { sentTexts } from "./recall"
import { useDockedWork } from "./use-docked-work"
import { useChatStream } from "./use-chat-stream"
import { OpenFileContext, type OpenFile } from "./open-file-context"
import { QueuedStrip } from "./queued-strip"

// ChatBody is a chat without its header: the conversation, what waits for the owner, and the composer. It fills
// the page of one chat and a pane of the grid alike. A message sent shows at once, before the stream keeps it, as
// t3code's optimisticUserMessages.ts: it waits under a seq of its own until the server answers with its seq, and
// goes when the kept one arrives; a refused send takes it away again.
//
// Files dropped anywhere on the chat are attached to the message being written (issue #343, AttachDrop);
// onOpenFile opens an attached file in the pane, onOpenArtifact an artifact beside the chat (issue #402), and
// onOpenBrowserTab a browser tab where the pane opens it (issue #703), and onOpenTake a take the phone was filmed in
// (issue #817).
export function ChatBody({
  chatId,
  onOpenFile,
  onOpenArtifact,
  onOpenBrowserTab,
  onOpenTake,
}: {
  chatId: string
  onOpenFile?: OpenFile
  onOpenArtifact?: OpenArtifact
  onOpenBrowserTab?: (tab: string) => void
  onOpenTake?: (take: string) => void
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const refreshBots = useRefreshBots()
  const { view, loadEarlier, loadingEarlier } = useChatStream(chatId)
  const chat = useChat(chatId)
  const [sending, setSending] = useState<ChatMessage[]>([])
  // A queued message taken back to edit goes into the composer (issue #497).
  const [put, setPut] = useState<{ text: string; at: number }>()
  // The chat's draft (issue #433): its files are put back as attached, and ↑ brings back what it sent, those the
  // stream kept and those on their way.
  const draft = { key: chatDraft(chatId) }
  const attachments = useAttachments(chatId, undefined, readDraft(draft.key))
  const sent = sentTexts([
    ...view.messages,
    ...sending.filter((m) => !view.messages.some((k) => k.seq === m.seq)),
  ])
  // What waits above the composer: the Approvals, the questions of this chat's Runs and the Runs themselves.
  const approvals = usePending({ chatId })
  const work = useDockedWork(chatId, wakeCount(view.messages), approvals.data?.length ?? 0)
  const main = useMainBot()
  const bot = chat.data?.bot ?? main
  // What the chat's Bot is called, for its replies and the message box; a chat keeps its Bot's id (issue #466).
  const botName = useBotName(bot)
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
    const key = getChatQueryKey({ path: { chat_id: chatId } })
    const before = queryClient.getQueryData<Chat>(key)
    if (before) queryClient.setQueryData<Chat>(key, { ...before, ...next })

    try {
      const { data } = await updateChat({ path: { chat_id: chatId }, body: next })
      queryClient.setQueryData(key, data)
    } catch {
      if (before) queryClient.setQueryData(key, before)
    }
  }

  // /new starts a new chat with the chat's Bot, which becomes its main chat, and opens it (issue #471), as Lark's
  // /new and `hypomnema chat /new`: the message is not sent, and this chat stays as it is.
  async function startNew() {
    const { data } = await createChat({ body: { bot } })
    void refreshBots()
    void queryClient.invalidateQueries({ queryKey: everyCall(listChatsQueryKey()) })
    await navigate({ to: "/chats/$chatId", params: { chatId: data.id } })
  }

  async function send(text: string, files: ChatAttachment[]) {
    if (files.length === 0 && text.trim().toLowerCase() === "/new") return startNew()

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
      const { data, response } = await sendChatMessage({
        path: { chat_id: chatId },
        body: { text, ...(files.length > 0 && { attachments: files }) },
      })
      // 202: pi works, so the message waits in the queue, which the stream shows above the composer (issue #497).
      if (response.status === 202) setSending((list) => list.filter((m) => m !== shown))
      else setSending((list) => list.map((m) => (m === shown ? (data as ChatMessage) : m)))
    } catch (err) {
      setSending((list) => list.filter((m) => m !== shown))
      throw err
    }

    void queryClient.invalidateQueries({ queryKey: listChatsQueryKey() })
  }

  // The queue's rows (issue #497): Edit takes the message out and puts it back in the composer with its files, as
  // Omnigent's does; Send now moves it into the chat, which steers pi; Remove drops it. One the server already
  // gave to pi is a 404, and the stream's next queue frame shows it gone.
  async function edit(m: QueuedChatMessage) {
    try {
      const { data } = await removeQueuedChatMessage({
        path: { chat_id: chatId, queue_id: m.id },
      })
      setPut({ text: data.text, at: Date.now() })
      if (data.attachments?.length) attachments.draft.restore(data.attachments)
    } catch {
      // Gone to pi meanwhile: nothing to edit.
    }
  }

  return (
    <OpenFileContext value={onOpenFile ?? null}>
      <OpenArtifactContext value={onOpenArtifact ?? null}>
        <OpenBrowserTabContext
          value={onOpenBrowserTab ? { chat: chatId, open: onOpenBrowserTab } : null}
        >
          <OpenTakeContext value={onOpenTake ?? null}>
            <DockContext value={work.dock}>
              <AttachDrop
                attachments={attachments}
                className="flex min-h-0 min-w-0 flex-1 flex-col"
              >
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
                  botName={botName}
                  setups={setups}
                  onLoadEarlier={() => void loadEarlier()}
                  loadingEarlier={loadingEarlier}
                  empty={
                    <p className="text-center text-sm text-muted-foreground">
                      Send a message to start.
                    </p>
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
                  <QueuedStrip
                    queue={view.queue}
                    onEdit={(m) => void edit(m)}
                    onSendNow={(m) =>
                      void sendQueuedChatMessage({ path: { chat_id: chatId, queue_id: m.id } })
                    }
                    onRemove={(m) =>
                      void removeQueuedChatMessage({ path: { chat_id: chatId, queue_id: m.id } })
                    }
                  />
                  <Composer
                    put={put}
                    working={view.working}
                    botName={botName}
                    onSend={send}
                    onStop={() => void abortChat({ path: { chat_id: chatId } })}
                    attachments={attachments}
                    draft={draft}
                    sent={sent}
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
          </OpenTakeContext>
        </OpenBrowserTabContext>
      </OpenArtifactContext>
    </OpenFileContext>
  )
}
