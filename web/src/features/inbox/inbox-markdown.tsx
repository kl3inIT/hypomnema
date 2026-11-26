import { useQueries, useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  createContext,
  useContext,
  useState,
  type ComponentProps,
  type ComponentType,
  type ReactNode,
} from "react"
import { defaultComponents, type Components } from "streamdown"

import "@/api/client"
import {
  getChatOptions,
  getMeetingOptions,
  getRunOptions,
  getTaskOptions,
  getTodoOptions,
  listMessagingMessagesOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import type { MessagingMessage, Todo } from "@/api/generated/types.gen"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import Markdown from "@/features/chat/markdown"
import { useServices } from "@/features/settings/use-integrations"
import { firstLine } from "@/lib/text"

import { refOfHref, remarkInboxText, type Service } from "./inbox-text"

// What an id's link reads is kept a few minutes: a meeting's or a chat's title changes seldom.
const namesFor = 5 * 60_000
const linkClass = "text-link hover:underline"

const withParagraphs = [remarkInboxText({ paragraphs: true })]
const idsOnly = [remarkInboxText()]

// streamdown's own link, for every link that is not an id's.
const DefaultLink = defaultComponents.a as ComponentType<ComponentProps<"a">>

// ItemSource is where the item whose text is shown was read (issue #1186): which of its message ids the Inbox may
// link, and the Lark chat its Lark messages are in. Without it, a message's id stays as it was written.
const ItemSource = createContext<Todo["source"] | undefined>(undefined)

// InboxItemSource gives the item's source to the texts of its detail, its description and its comments.
export function InboxItemSource({
  source,
  children,
}: {
  source: Todo["source"]
  children: ReactNode
}) {
  return <ItemSource.Provider value={source}>{children}</ItemSource.Provider>
}

const components: Components = {
  a: ({ href, children, ...props }) => {
    const ref = refOfHref(href)
    if (!ref) {
      return (
        <DefaultLink href={href} {...props}>
          {children}
        </DefaultLink>
      )
    }

    switch (ref.kind) {
      case "mtg":
        return <MeetingLink id={ref.id} />
      case "cht":
        return <ChatLink id={ref.id} />
      case "tsk":
        return <TaskLink id={ref.id} />
      case "run":
        return <RunLink id={ref.id} />
      case "tdo":
        return <ItemLink id={ref.id} />
      case "lark_chat":
        return <LarkChatLink id={ref.id} />
      case "message":
        return ref.service === "lark" ? (
          <LarkMessageLink id={ref.id} />
        ) : (
          <KeptMessageLink service={ref.service} id={ref.id} />
        )
    }
  },
}

// InboxMarkdown is an Inbox item's text, its description (with paragraphs: each line its own paragraph, as it was
// written before it was Markdown) or a comment, rendered by the chat's Markdown with its ids as links named by what
// they name (issues #1173, #1186).
export function InboxMarkdown({
  text,
  paragraphs = false,
}: {
  text: string
  paragraphs?: boolean
}) {
  return (
    <Markdown
      text={text}
      streaming={false}
      remark={paragraphs ? withParagraphs : idsOnly}
      components={components}
    />
  )
}

// Each link is named by what it leads to, the id itself until that is read or when it cannot be; the id stays in
// its tooltip.

function MeetingLink({ id }: { id: string }) {
  const meeting = useQuery({
    ...getMeetingOptions({ path: { meeting_id: id } }),
    staleTime: namesFor,
    retry: false,
  })

  return (
    <Link
      to="/meetings/$meetingId"
      params={{ meetingId: id }}
      title={id}
      data-id={id}
      className={linkClass}
    >
      {meeting.data?.meeting.title || id}
    </Link>
  )
}

function ChatLink({ id }: { id: string }) {
  const chat = useQuery({
    ...getChatOptions({ path: { chat_id: id } }),
    staleTime: namesFor,
    retry: false,
  })

  return (
    <Link to="/chats/$chatId" params={{ chatId: id }} title={id} data-id={id} className={linkClass}>
      {chat.data?.title || id}
    </Link>
  )
}

function TaskLink({ id }: { id: string }) {
  const task = useQuery({
    ...getTaskOptions({ path: { task_id: id } }),
    staleTime: namesFor,
    retry: false,
  })

  return (
    <Link to="/team" search={{ task: id }} title={id} data-id={id} className={linkClass}>
      {task.data ? firstLine(task.data.spec, 80) : id}
    </Link>
  )
}

// RunLink leads to the Team page with the Run's first Task open, the Task panel that shows its Run; Team has no
// page of a Run's own.
function RunLink({ id }: { id: string }) {
  const run = useQuery({
    ...getRunOptions({ path: { run_id: id } }),
    staleTime: namesFor,
    retry: false,
  })
  const task = run.data?.tasks[0]?.id

  return (
    <Link to="/team" search={{ task }} title={id} data-id={id} className={linkClass}>
      {run.data ? firstLine(run.data.objective, 80) : id}
    </Link>
  )
}

// ItemLink opens another Inbox item, named by its title (issue #1186), as Linear names a linked issue: the Inbox
// selects it by its row's key (inbox-state.ts rowsOf).
function ItemLink({ id }: { id: string }) {
  const item = useQuery({
    ...getTodoOptions({ path: { todo_id: id } }),
    staleTime: namesFor,
    retry: false,
  })

  return (
    <Link to="/inbox" search={{ item: `todo:${id}` }} title={id} data-id={id} className={linkClass}>
      {item.data ? firstLine(item.data.title, 80) : id}
    </Link>
  )
}

// larkChatLink is the AppLink that opens a Lark chat the owner is in, in Lark, as the Lark CLI builds it
// (larksuite/cli shortcuts/im/chat_app_link.go, MIT): https://applink.larksuite.com/client/chat/open?openChatId=oc_…
// The owner's Lark is larksuite.com (integration/catalog.go).
function larkChatLink(chat: string) {
  return `https://applink.larksuite.com/client/chat/open?openChatId=${encodeURIComponent(chat)}`
}

function LarkChatLink({ id }: { id: string }) {
  return (
    <a
      href={larkChatLink(id)}
      target="_blank"
      rel="noreferrer"
      title={id}
      data-id={id}
      className={linkClass}
    >
      Lark chat
    </a>
  )
}

// LarkMessageLink opens the Lark chat the item was read in, where its Lark message is: the server keeps no Lark
// message, and Lark's AppLink to one message needs its position in the chat (message_app_link,
// larksuite/cli shortcuts/im/convert_lib), which an item does not keep. A Lark message's id in an item read
// elsewhere names no chat the Inbox knows, and stays as it was written.
function LarkMessageLink({ id }: { id: string }) {
  const source = useContext(ItemSource)
  const chat = source?.kind === "lark" && source.id?.startsWith("oc_") ? source.id : undefined
  if (!chat) return <>{id}</>

  return (
    <a
      href={larkChatLink(chat)}
      target="_blank"
      rel="noreferrer"
      title={id}
      data-id={id}
      className={linkClass}
    >
      message
    </a>
  )
}

// Account is one of the owner's messaging accounts linked as a device, by its service and its Connection's label.
type Account = { integration: Service; label: string }

// useAccounts is the owner's accounts at a service, the one the item was read with first.
function useAccounts(service: Service, source: Todo["source"] | undefined): Account[] {
  const services = useServices()
  const labels = (
    services.data?.find((s) => s.id === service && s.kind === "device")?.connections ?? []
  ).map((c) => c.label)
  const own = source?.kind === service ? source.account : undefined
  labels.sort((a, b) => Number(b === own) - Number(a === own))

  return labels.map((label) => ({ integration: service, label }))
}

// KeptMessageLink is a Zalo or WhatsApp message's id, a link named "message" once the server keeps a message of
// that id of one of the owner's accounts there (issue #1186); any other number stays as it was written. Neither
// service has an address of one message, nor the app a page of their chats: the link opens the message in its chat
// beside it, the messages before it and the message itself, as the server keeps them, as Chatwoot opens a
// conversation at a message.
function KeptMessageLink({ service, id }: { service: Service; id: string }) {
  const source = useContext(ItemSource)
  const accounts = useAccounts(service, source)
  const found = useQueries({
    queries: (source ? accounts : []).map((path) => ({
      ...listMessagingMessagesOptions({ path, query: { id, limit: 1 } }),
      staleTime: Infinity,
      retry: false,
    })),
    combine: (lists) => {
      for (const [i, list] of lists.entries()) {
        const message = list.data?.data.find((m) => m.id === id)
        if (message) return { account: accounts[i]!, message }
      }

      return undefined
    },
  })
  if (!found) return <>{id}</>

  return <MessageAt account={found.account} message={found.message} />
}

// What the message's chat shows: it and the few before it.
const chatShown = 6

const at = new Intl.DateTimeFormat("en-GB", {
  day: "2-digit",
  month: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
})

const serviceNames: Record<Service, string> = { zalo: "Zalo", whatsapp: "WhatsApp", lark: "Lark" }

function MessageAt({ account, message }: { account: Account; message: MessagingMessage }) {
  const [open, setOpen] = useState(false)
  // Before the second after it: WhatsApp keeps a message's time in seconds.
  const before = new Date(Date.parse(message.at) + 1000).toISOString()
  const chat = useQuery({
    ...listMessagingMessagesOptions({
      path: account,
      query: { chat: message.chat, before, limit: chatShown },
    }),
    enabled: open,
    staleTime: Infinity,
    retry: false,
  })
  const list = chat.data?.data ?? []
  const shown = [...(list.some((m) => m.id === message.id) ? list : [message, ...list])].reverse()
  const said = `${senderOf(message)}, ${at.format(new Date(message.at))}`

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <button
            type="button"
            data-id={message.id}
            aria-label={`Message from ${said}`}
            title={said}
            className={`${linkClass} cursor-pointer`}
          />
        }
      >
        message
      </PopoverTrigger>
      <PopoverContent align="start" data-slot="message-at">
        <div className="flex flex-col gap-2">
          <div className="text-xs text-muted-foreground">
            {serviceNames[account.integration]} ·{" "}
            <span className="font-medium text-foreground">{message.chat_name || message.chat}</span>
          </div>
          <ol
            aria-label={message.chat_name || message.chat}
            className="flex max-h-72 flex-col gap-1.5 overflow-y-auto"
          >
            {shown.map((m) => (
              <li
                key={m.id}
                aria-current={m.id === message.id ? "true" : undefined}
                className={
                  m.id === message.id
                    ? "rounded-md bg-muted px-2 py-1"
                    : "px-2 py-1 text-muted-foreground"
                }
              >
                <div className="text-xs">
                  <span className="font-medium text-foreground">{senderOf(m)}</span> ·{" "}
                  <time dateTime={m.at} className="tabular-nums">
                    {at.format(new Date(m.at))}
                  </time>
                </div>
                <div className="text-sm [overflow-wrap:anywhere] whitespace-pre-wrap">
                  {wordsOf(m)}
                </div>
              </li>
            ))}
          </ol>
        </div>
      </PopoverContent>
    </Popover>
  )
}

function senderOf(m: MessagingMessage) {
  return m.from_me ? "You" : m.sender_name || m.sender
}

// wordsOf is what a message says, its text, else its caption or file, else what kind of media it is. It is the
// owner's correspondents' words: shown as text, never as Markdown.
function wordsOf(m: MessagingMessage) {
  if (m.revoked) return "(removed)"

  return m.text || m.caption || m.file_name || (m.media_type ? `(${m.media_type})` : "")
}
