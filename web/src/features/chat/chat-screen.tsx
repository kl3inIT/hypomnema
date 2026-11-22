import { useNavigate } from "@tanstack/react-router"
import { useState } from "react"

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { AvatarName } from "@/components/agent-avatar"
import { botNameOf, mainBotOf } from "@/features/bots/bots-state"
import { useBots } from "@/features/bots/use-bots"
import { AppSidebar } from "@/features/shell/app-shell"
import { MenuButton, PageHeader } from "@/components/page-header"
import { useSoloChat } from "@/features/workbench/solo-chat"
import { SoloPane } from "@/features/workbench/solo-pane"

import { AttachDrop } from "./attach-drop"
import { useAttachments } from "./attachments"
import { ChatModelControls } from "./chat-model-picker"
import { useDraftChoice } from "./chat-model-state"
import { newChatId } from "./chat-id"
import { Composer } from "./composer"
import { newChatDraft, readDraft } from "./drafts"
import { useStartChat } from "./start-chat"

// The chat with pi, laid out as t3code's app (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): the app's sidebar (features/shell), and beside it a column with a quiet
// header (ChatView.tsx, ChatHeader.tsx), the conversation and the composer. On a phone the sidebar hides and the
// header's ≡ opens it as a drawer (AppDrawer, issue #604). Without a chat open, the start
// screen's composer starts one (chat/DraftHeroHeadline.tsx). A chat talks with one Bot (issue #169): the start
// screen offers the Bots when there are several, and an open chat's header names its Bot. The start screen chooses
// its chat's id when it opens (issue #357), so that files are attached there before the chat is made.
export function ChatScreen({ chatId, bot }: { chatId?: string; bot?: string }) {
  const viewed = useSoloChat(chatId)

  return (
    <>
      {/* The sidebar knows the chat in front of the page's pane as the one being viewed (issue #923). */}
      <AppSidebar currentChat={viewed} />
      {chatId ? <OpenChat key={chatId} chatId={chatId} /> : <NewChat initialBot={bot} />}
    </>
  )
}

function NewChat({ initialBot }: { initialBot?: string }) {
  const navigate = useNavigate()
  // The Bot the new chat talks with. With one Bot there is nothing to choose and the server takes the first.
  // The page's draft (issue #433) keeps the chat's id it chose and the Bot picked, whose folder holds its files.
  const [saved] = useState(() => readDraft(newChatDraft))
  const listed = useBots().data
  const bots = listed ?? []
  const main = mainBotOf(listed)
  const [picked, setPicked] = useState(initialBot ?? saved.bot ?? "")
  const choice = bots.length > 1
  // Until the Bots are read, the one picked stands, so that the files of the draft do not move meanwhile.
  // A Bot is picked by its id (issue #466); one the list does not have, a draft's from before, is the main Bot.
  const bot = !listed || bots.some((b) => b.id === picked) ? picked : main
  const [chatId] = useState(() => saved.chat ?? newChatId())
  const attachments = useAttachments(chatId, bot, { files: saved.files, bot: saved.bot ?? bot })
  const [model, setModel] = useDraftChoice(bot)

  // Before the Bots are read, the one picked is named: whether there is a choice is not known yet (issue #1055).
  const named = listed ? (choice ? bot : undefined) : bot || undefined
  const start = useStartChat(chatId, named, model, (chat) =>
    navigate({ to: "/chats/$chatId", params: { chatId: chat.id } }),
  )

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />
      <AttachDrop
        attachments={attachments}
        className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 sm:px-12"
      >
        <div className="mx-auto flex w-full max-w-5xl flex-1 flex-col items-center justify-center gap-6 py-8">
          <h1 className="w-full text-center text-2xl font-normal tracking-tight text-foreground sm:text-3xl">
            What should be built?
          </h1>
          {choice && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              Chat with
              <Select value={bot} onValueChange={(value) => value && setPicked(value)}>
                <SelectTrigger aria-label="Bot" size="sm">
                  <SelectValue>
                    {(value: string) => <AvatarName name={botNameOf(bots, value)} />}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {bots.map((b) => (
                    <SelectItem key={b.id} value={b.id}>
                      <AvatarName name={b.name} />
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <Composer
            onSend={(text, files) => start.mutateAsync({ text, files })}
            onStop={() => {}}
            working={false}
            botName={botNameOf(bots, bot)}
            attachments={attachments}
            draft={{ key: newChatDraft, chat: chatId, bot }}
            controls={<ChatModelControls bot={bot} choice={model} onChange={setModel} />}
          />
          <p className="max-w-(--chat-content-max-width) text-center text-sm text-muted-foreground">
            pi plans the work, gives it to workers on your machines, and reports back here.
          </p>
        </div>
      </AttachDrop>
    </main>
  )
}

// OpenChat is a chat's own page: its pane, whose tabs are the top of the page with no header above them (issue
// #235).
function OpenChat({ chatId }: { chatId: string }) {
  return (
    <main className="relative flex min-w-0 flex-1 flex-col">
      <SoloPane chat={chatId} start={<MenuButton />} />
    </main>
  )
}
