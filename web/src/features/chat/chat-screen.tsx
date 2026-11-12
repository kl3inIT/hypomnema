import { useNavigate } from "@tanstack/react-router"
import { useState } from "react"

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { firstBot } from "@/features/bots/bots-state"
import { useBots } from "@/features/bots/use-bots"
import {
  AppSidebar,
  ChatList,
  MenuLink,
  PageHeader,
  Places,
  SettingsLink,
  SignOut,
} from "@/features/shell/app-shell"
import { SoloPane } from "@/features/workbench/solo-pane"

import { Composer } from "./composer"
import { useStartChat } from "./start-chat"

// The chat with pi, laid out as t3code's app (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): the app's sidebar (features/shell), and beside it a column with a quiet
// header (ChatView.tsx, ChatHeader.tsx), the conversation and the composer. On a phone the sidebar hides and the
// header's menu opens the start screen, which lists the places and the chats. Without a chat open, the start
// screen's composer starts one (chat/DraftHeroHeadline.tsx). A chat talks with one Bot (issue #169): the start
// screen offers the Bots when there are several, and an open chat's header names its Bot.
export function ChatScreen({ chatId }: { chatId?: string }) {
  return (
    <>
      <AppSidebar currentChat={chatId} />
      {chatId ? <OpenChat key={chatId} chatId={chatId} /> : <NewChat />}
    </>
  )
}

function NewChat() {
  const navigate = useNavigate()
  // The Bot the new chat talks with. With one Bot there is nothing to choose and the server takes the first.
  const bots = useBots().data ?? []
  const [picked, setPicked] = useState(firstBot)
  const choice = bots.length > 1
  const bot = bots.some((b) => b.name === picked) ? picked : firstBot

  const start = useStartChat(choice ? bot : undefined, (chat) =>
    navigate({ to: "/chats/$chatId", params: { chatId: chat.id } }),
  )

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 sm:px-12">
        <div className="mx-auto flex w-full max-w-5xl flex-1 flex-col items-center justify-center gap-6 py-8">
          <h1 className="w-full text-center text-2xl font-normal tracking-tight text-foreground sm:text-3xl">
            What should be built?
          </h1>
          {choice && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              Chat with
              <Select value={bot} onValueChange={(value) => value && setPicked(value)}>
                <SelectTrigger aria-label="Bot" size="sm">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {bots.map((b) => (
                    <SelectItem key={b.name} value={b.name}>
                      {b.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <Composer onSend={(text) => start.mutateAsync(text)} onStop={() => {}} working={false} />
          <p className="max-w-(--chat-content-max-width) text-center text-sm text-muted-foreground">
            pi plans the work, gives it to workers on your machines, and reports back here.
          </p>
          <div className="w-full max-w-(--chat-content-max-width) md:hidden">
            <Places />
            <ChatList label="Recent chats" />
            <div className="mt-2 flex flex-col gap-px">
              <SettingsLink />
              <SignOut />
            </div>
          </div>
        </div>
      </div>
    </main>
  )
}

// OpenChat is a chat's own page: its pane, whose tabs are the top of the page with no header above them (issue
// #235).
function OpenChat({ chatId }: { chatId: string }) {
  return (
    <main className="relative flex min-w-0 flex-1 flex-col">
      <SoloPane chat={chatId} start={<MenuLink />} />
    </main>
  )
}
