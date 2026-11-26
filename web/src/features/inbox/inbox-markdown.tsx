import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import type { ComponentProps, ComponentType } from "react"
import { defaultComponents, type Components } from "streamdown"

import "@/api/client"
import {
  getChatOptions,
  getMeetingOptions,
  getRunOptions,
  getTaskOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import Markdown from "@/features/chat/markdown"
import { firstLine } from "@/lib/text"

import { idOfHref, remarkInboxText } from "./inbox-text"

// What an id's link reads is kept a few minutes: a meeting's or a chat's title changes seldom.
const namesFor = 5 * 60_000
const linkClass = "text-link hover:underline"

const withBreaks = [remarkInboxText({ breaks: true })]
const idsOnly = [remarkInboxText()]

// streamdown's own link, for every link that is not an id's.
const DefaultLink = defaultComponents.a as ComponentType<ComponentProps<"a">>

const components: Components = {
  a: ({ href, children, ...props }) => {
    const named = idOfHref(href)
    if (!named) {
      return (
        <DefaultLink href={href} {...props}>
          {children}
        </DefaultLink>
      )
    }

    switch (named.kind) {
      case "mtg":
        return <MeetingLink id={named.id} />
      case "cht":
        return <ChatLink id={named.id} />
      case "tsk":
        return <TaskLink id={named.id} />
      case "run":
        return <RunLink id={named.id} />
    }
  },
}

// InboxMarkdown is an Inbox item's text, its description (with breaks: each line its own, as it was written before
// it was Markdown) or a comment, rendered by the chat's Markdown with its ids as links named by what they name
// (issue #1173).
export function InboxMarkdown({ text, breaks = false }: { text: string; breaks?: boolean }) {
  return (
    <Markdown
      text={text}
      streaming={false}
      remark={breaks ? withBreaks : idsOnly}
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
