import { Link } from "@tanstack/react-router"
import { Bot, ChevronRight, Search, User, Users } from "lucide-react"
import { useDeferredValue, useState } from "react"

import type {
  InboxChat,
  InboxChatKind,
  InboxChatSource,
  InboxChatState,
} from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { PageHeader, PageHeading } from "@/components/page-header"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { errorText } from "@/lib/server-error"
import { cn } from "@/lib/utils"

import { BulkChatActions } from "./bulk-chat-actions"
import { defaultsLine, kindNames, sourceKey, sourceTitle, why } from "./chats-read-state"
import type { Source } from "./inbox-state"
import { SourceIcon } from "./source-icon"
import { useChatSources, useChats, useSetChat } from "./use-chats-read"

// ChatsReadScreen is which of the owner's chats the secretary reads (issue #1270), opened from the gear on the Inbox
// page as the meetings' settings open from theirs (#1121): a tab per account with its counts, a search by name
// (wacli's `chats list --query`), the kind and state filters as the Machines page's segmented controls, and a switch
// per chat with why it is in that state. The flag is the server's, per account and chat (inbox-zero's per-account
// exclude items); a chat switched off is still kept and read on request.
export function ChatsReadScreen() {
  const sources = useChatSources()
  const all = sources.data?.data ?? []
  const [tab, setTab] = useState<string>()
  const current = tab ?? (all[0] ? sourceKey(all[0]) : undefined)

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader
        title={
          <span className="flex min-w-0 items-center gap-1.5">
            <Link to="/inbox" className="text-muted-foreground hover:text-foreground">
              Inbox
            </Link>
            <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/60" aria-hidden />
            <span className="truncate">Chats</span>
          </span>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-8 sm:px-5">
        <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-4 pt-2">
          <PageHeading
            title="Chats the secretary reads"
            description="The triage reads your new messages in the chats switched on. A chat switched off is still kept, and a Bot reads it when you ask."
          />
          {sources.data && (
            <p data-testid="chat-defaults" className="text-sm text-muted-foreground">
              {defaultsLine(sources.data.defaults)}
            </p>
          )}
          {sources.error ? (
            <ErrorAlert error={sources.error} />
          ) : sources.isPending ? (
            <div className="flex justify-center py-10">
              <Spinner />
            </div>
          ) : !all.length ? (
            <p className="py-10 text-center text-sm text-muted-foreground">
              No Zalo, WhatsApp or Lark account of yours is connected.
            </p>
          ) : (
            <Tabs value={current} onValueChange={(v) => setTab(String(v))}>
              <div className="max-w-full overflow-x-auto overflow-y-hidden">
                <TabsList>
                  {all.map((s) => (
                    <TabsTrigger key={sourceKey(s)} value={sourceKey(s)}>
                      <SourceIcon source={s.integration as Source} />
                      {sourceTitle(s, all)}
                      {s.counts && (
                        <span className="text-muted-foreground tabular-nums">{s.counts.all}</span>
                      )}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </div>
              {all.map((s) => (
                <TabsContent key={sourceKey(s)} value={sourceKey(s)}>
                  {s.problem ? (
                    <p role="alert" className="py-6 text-sm text-destructive">
                      Its chats cannot be listed now: {s.problem}
                    </p>
                  ) : (
                    <SourceChats source={s} title={sourceTitle(s, all)} />
                  )}
                </TabsContent>
              ))}
            </Tabs>
          )}
        </div>
      </div>
    </main>
  )
}

const kinds: (InboxChatKind | "all")[] = ["all", "direct", "group", "bot"]
const states: (InboxChatState | "all")[] = ["all", "read", "skipped"]
const stateNames: Record<InboxChatState | "all", string> = {
  all: "All",
  read: "Read",
  skipped: "Skipped",
}

// SourceChats is one account's chats: the search and the filters, then a card of rows.
function SourceChats({ source, title }: { source: InboxChatSource; title: string }) {
  const [query, setQuery] = useState("")
  const q = useDeferredValue(query.trim())
  const [kind, setKind] = useState<InboxChatKind | "all">("all")
  const [state, setState] = useState<InboxChatState | "all">("all")
  const chats = useChats(source.integration, source.label, {
    q,
    kind: kind === "all" ? undefined : kind,
    state: state === "all" ? undefined : state,
  })
  const counts = chats.data?.counts ?? source.counts
  const filterCounts = chats.data?.search_counts ?? counts

  return (
    <div className="flex flex-col gap-3 pt-3">
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-60 max-sm:w-full">
          <InputGroup>
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              aria-label={`Search the chats of ${title}`}
              placeholder="Search by name…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </InputGroup>
        </div>
        <Segmented
          legend="Kind"
          value={kind}
          options={kinds}
          name={(k) => (k === "all" ? "All" : kindNames[k])}
          count={(k) => filterCounts?.[k]}
          onChange={setKind}
        />
        <Segmented
          legend="State"
          value={state}
          options={states}
          name={(s) => stateNames[s]}
          count={(s) => filterCounts?.[s]}
          onChange={setState}
        />
      </div>
      <BulkChatActions
        source={source}
        title={title}
        chats={chats.data?.data ?? []}
        disabled={
          Boolean(chats.error) || chats.isFetching || chats.isPlaceholderData || query.trim() !== q
        }
      />
      <section
        aria-label={`Chats of ${title}`}
        className="w-full overflow-hidden rounded-xl border bg-card text-card-foreground"
      >
        <header className="flex items-center gap-2 border-b border-border/60 bg-muted/30 px-4 py-2 text-xs text-muted-foreground">
          <SourceIcon source={source.integration as Source} />
          <span className="min-w-0 flex-1 truncate">{source.account || source.label}</span>
          {counts && (
            <span className="tabular-nums">
              {counts.read} read · {counts.skipped} skipped
            </span>
          )}
        </header>
        {chats.error ? (
          <div className="p-4">
            <ErrorAlert error={chats.error} />
          </div>
        ) : chats.isPending ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : !chats.data.data.length ? (
          <p className="py-8 text-center text-sm text-muted-foreground">No chat matches.</p>
        ) : (
          <ul>
            {chats.data.data.map((chat) => (
              <ChatRow key={chat.id} source={source} chat={chat} />
            ))}
          </ul>
        )}
      </section>
      {chats.data && chats.data.matched > chats.data.data.length && (
        <p className="text-center text-xs text-muted-foreground">
          {chats.data.data.length} of {chats.data.matched} shown; search to find the others.
        </p>
      )}
    </div>
  )
}

// Segmented is the Machines page's segmented control (machines-screen.tsx, its layout), each option with its count.
function Segmented<T extends string>({
  legend,
  value,
  options,
  name,
  count,
  onChange,
}: {
  legend: string
  value: T
  options: T[]
  name: (o: T) => string
  count: (o: T) => number | undefined
  onChange: (o: T) => void
}) {
  return (
    <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
      <legend className="sr-only">{legend}</legend>
      {options.map((o) => (
        <button
          key={o}
          type="button"
          aria-pressed={value === o}
          onClick={() => onChange(o)}
          className="rounded-md px-2.5 py-1 text-xs text-muted-foreground aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
        >
          {name(o)}
          {count(o) !== undefined && (
            <span className="ms-1 tabular-nums opacity-70">{count(o)}</span>
          )}
        </button>
      ))}
    </fieldset>
  )
}

const kindIcons = { direct: User, group: Users, bot: Bot }

// ChatRow is one chat: its kind's icon, its name, why it is in its state, and the switch.
function ChatRow({ source, chat }: { source: InboxChatSource; chat: InboxChat }) {
  const set = useSetChat()
  const Icon = kindIcons[chat.kind]
  const reads = set.isPending && set.variables ? set.variables.body.reads : chat.reads
  const switchId = `chat-${source.integration}-${source.label}-${chat.id}`

  return (
    <li
      data-testid="secretary-chat"
      className={cn(
        "flex items-center gap-3 border-b border-border/60 px-4 py-2.5 last:border-b-0",
        !reads && "text-foreground/70",
      )}
    >
      <Icon className="size-4 shrink-0 text-muted-foreground" aria-label={kindNames[chat.kind]} />
      <label htmlFor={switchId} className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-sm font-medium">{chat.name || chat.id}</span>
        <span className="truncate text-xs text-muted-foreground">
          {kindNames[chat.kind]} · {reads ? "Read" : "Skipped"} · {why(chat)}
        </span>
      </label>
      <Switch
        id={switchId}
        checked={reads}
        disabled={set.isPending}
        aria-label={`The secretary reads ${chat.name || chat.id}`}
        onCheckedChange={(on) =>
          set.mutate(
            {
              path: { integration: source.integration, label: source.label, chat: chat.id },
              body: { reads: on },
            },
            {
              onSuccess: () =>
                toast.add({
                  title: on
                    ? `The secretary reads ${chat.name || chat.id}`
                    : `The secretary skips ${chat.name || chat.id}`,
                  type: "success",
                }),
              onError: (error) => toast.add({ title: errorText(error), type: "error" }),
            },
          )
        }
      />
    </li>
  )
}
