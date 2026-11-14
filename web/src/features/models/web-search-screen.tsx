import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  CircleDollarSign,
  Clock,
  Globe,
  GripVertical,
  Info,
  KeyRound,
  Plus,
  Power,
  Timer,
  Trash2,
  UserRound,
  type LucideIcon,
} from "lucide-react"
import { useId, useState, type DragEvent, type ReactNode } from "react"
import { z } from "zod"

import "@/api/client"
import {
  addWebSearchKey,
  getWebSearch,
  removeWebSearchKey,
  setWebSearchOrder,
  setWebSearchProvider,
} from "@/api/generated/sdk.gen"
import type {
  WebSearch,
  WebSearchKey,
  WebSearchKeyedProvider,
  WebSearchLink,
  WebSearchOrder,
} from "@/api/generated/types.gen"
import {
  ExaIcon,
  FirecrawlIcon,
  ParallelIcon,
  TavilyIcon,
  TinyFishIcon,
  YouIcon,
} from "@/components/icons/brand-icons"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { FieldGroup } from "@/components/ui/field"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ProviderMark } from "@/features/agents/model-mark"
import { Meter } from "@/features/machines/machine-cells"
import {
  ConfirmDialog,
  ErrorAlert,
  SettingsSection,
  TextField,
} from "@/features/settings/settings-parts"
import { SettingsPage } from "@/features/settings/settings-shell"
import { said } from "@/features/settings/use-integrations"
import { since } from "@/lib/time"

import {
  balanceShare,
  balanceText,
  callsText,
  isKeyed,
  keyPages,
  keyState,
  linkState,
  moved,
  providerName,
  sourceText,
  spendText,
} from "./web-search-state"

const webSearchKey = ["web-search"] as const

// WebSearchScreen is Settings → Gateway → Web search (issues #354, #393): the search chain in the order a search
// tries it, in three parts. The providers with the owner's keys come first, in the order the owner drags them to,
// as search-rotation's dashboard reorders its engines by a handle (static/app.js, the .handle drag); each lists
// its keys, which a search uses least recently used first (mcp-nexus's Tavily pool), with each key's state, what it
// used and its balance where the provider tells one, as search-rotation's engine cards show them. A key is added in
// a dialog, as OmniRoute's and 9router's "Add API Key" modals take a provider's key in a password field, and never
// shown again. The keyless providers come next, taken in turn (Hermes's keyless ring), then the subscriptions,
// whose account is added on the Models page. The rows follow the Models page's account rows, with the Machines
// page's meter for a share. The page does what `hypomnema web providers|keys|order` does.
export function WebSearchScreen() {
  const chain = useQuery({
    queryKey: webSearchKey,
    queryFn: async () => (await getWebSearch()).data,
    // A rest ends on its own, so the chain is read again now and then while the page is open.
    refetchInterval: 30_000,
  })

  return (
    <SettingsPage
      title="Web search"
      description="How agents search the web: the providers in the order a search tries them, and how much of each is used."
    >
      <ErrorAlert error={chain.error} />
      {chain.isPending ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : (
        chain.data && <Chain web={chain.data} />
      )}
    </SettingsPage>
  )
}

function Chain({ web }: { web: WebSearch }) {
  const keyed = web.chain.filter((l) => l.source === "key")
  const keyless = web.chain.filter((l) => l.source === "public")
  const subscriptions = web.chain.filter((l) => l.source === "subscription")

  return (
    <div className="flex flex-col gap-8">
      <SettingsSection
        id="search-keyed"
        icon={KeyRound}
        title="With your keys"
        note="Asked first, in this order; drag a provider by its handle to change it. A provider's keys are used in turn, the one used longest ago first. A key that is rate limited, refused or out of credits rests on its own while the others go on."
      >
        <KeyedList links={keyed} />
      </SettingsSection>
      <SettingsSection
        id="search-keyless"
        icon={Globe}
        title="Without a key"
        note="Asked next. Each search starts one further along this list, so that their limits, shared by everyone behind this address, are spread."
      >
        <ItemGroup>
          {keyless.map((link) => (
            <LinkRow key={`public-${link.provider}`} link={link} />
          ))}
        </ItemGroup>
      </SettingsSection>
      <SettingsSection
        id="search-subscriptions"
        icon={UserRound}
        title="From your subscriptions"
        note="Asked last, with the web search of an account you added on the Models page."
      >
        <ItemGroup>
          {subscriptions.map((link) => (
            <LinkRow key={`subscription-${link.provider}`} link={link} />
          ))}
        </ItemGroup>
      </SettingsSection>
    </div>
  )
}

// useChange runs a change of the chain and shows the chain the server answers with.
function useChange<T>(change: (input: T) => Promise<WebSearch>, done: (input: T) => string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: change,
    onSuccess: (web, input) => {
      queryClient.setQueryData(webSearchKey, web)
      said(done(input))
    },
  })
}

// useSwitch turns a provider on or off.
function useSwitch(link: WebSearchLink) {
  const name = providerName(link.provider)

  return useChange(
    async (on: boolean) =>
      (await setWebSearchProvider({ path: { provider: link.provider }, body: { on } })).data,
    (on) => (on ? `${name} is on` : `${name} is off`),
  )
}

// KeyedList is the keyed providers in their order. A provider is dragged by its handle and dropped on another's
// place; the new order is sent at once and the server's answer shown.
function KeyedList({ links }: { links: WebSearchLink[] }) {
  const [dragged, setDragged] = useState<WebSearchKeyedProvider | null>(null)
  const [over, setOver] = useState<WebSearchKeyedProvider | null>(null)
  const order = links.map((l) => l.provider).filter(isKeyed)
  const reorder = useChange(
    async (providers: WebSearchKeyedProvider[]) =>
      (await setWebSearchOrder({ body: { providers } as WebSearchOrder })).data,
    (providers) =>
      providers[0] ? `${providerName(providers[0])} is asked first` : "The order is kept",
  )

  const drop = (to: WebSearchKeyedProvider) => {
    const from = dragged
    setDragged(null)
    setOver(null)

    if (from && from !== to) reorder.mutate(moved(order, from, to))
  }

  return (
    <div className="flex flex-col gap-2.5">
      <ErrorAlert error={reorder.error} />
      {links.map((link, index) => {
        const provider = link.provider
        if (!isKeyed(provider)) return null

        return (
          <div
            key={provider}
            data-dragging={dragged === provider || undefined}
            data-over={(over === provider && dragged !== provider) || undefined}
            className="rounded-lg outline-offset-2 data-dragging:opacity-50 data-over:outline-2 data-over:outline-ring"
            onDragOver={(event: DragEvent) => {
              if (!dragged) return
              event.preventDefault()
              setOver(provider)
            }}
            onDragLeave={() => setOver((now) => (now === provider ? null : now))}
            onDrop={(event: DragEvent) => {
              event.preventDefault()
              drop(provider)
            }}
          >
            <KeyedRow
              link={link}
              order={index + 1}
              busy={reorder.isPending}
              onDragStart={() => setDragged(provider)}
              onDragEnd={() => {
                setDragged(null)
                setOver(null)
              }}
            />
          </div>
        )
      })}
    </div>
  )
}

// KeyedRow is one keyed provider: its place, its mark, its state, a key to add, its switch, and its keys.
function KeyedRow({
  link,
  order,
  busy,
  onDragStart,
  onDragEnd,
}: {
  link: WebSearchLink
  order: number
  busy: boolean
  onDragStart: () => void
  onDragEnd: () => void
}) {
  const name = providerName(link.provider)
  const state = linkState(link)
  const toggle = useSwitch(link)
  const keys = link.keys ?? []
  const provider = link.provider as WebSearchKeyedProvider

  return (
    <div className={link.on ? undefined : "opacity-70"}>
      <Item variant="muted" size="sm" aria-label={`${name}, ${sourceText(link)}`}>
        {/* The handle is its own design: a grip that is dragged, not pressed (search-rotation's .handle). */}
        <span
          draggable={!busy}
          onDragStart={(event) => {
            event.dataTransfer.effectAllowed = "move"
            event.dataTransfer.setData("text/plain", provider)
            onDragStart()
          }}
          onDragEnd={onDragEnd}
          className="-ms-1 cursor-grab self-start pt-0.5 text-muted-foreground active:cursor-grabbing pointer-coarse:hidden"
          title="Drag to change the order"
          aria-label={`Drag ${name} to change the order`}
        >
          <GripVertical className="size-4" aria-hidden />
        </span>
        <span
          className="self-start pt-0.5 text-xs text-muted-foreground tabular-nums"
          title="A search tries the providers in this order"
        >
          #{order}
        </span>
        <Mark provider={link.provider} />
        <ItemContent>
          <ItemTitle>{name}</ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <StateBadge state={state} icon={stateIcon(link.state)} />
              {keys.length > 0 && (
                <Fact icon={KeyRound}>{keys.length === 1 ? "1 key" : `${keys.length} keys`}</Fact>
              )}
            </span>
          </ItemDescription>
          <ErrorAlert error={toggle.error} />
        </ItemContent>
        <div className="self-start">
          <ItemActions>
            <AddKeyDialog provider={provider} first={keys.length === 0} />
            <InUse link={link} toggle={toggle} />
          </ItemActions>
        </div>
      </Item>
      {link.provider === "tinyfish" && <TinyFishNote />}
      {keys.length > 0 && (
        <ul
          className="ms-6 mt-1.5 flex flex-col gap-1.5 border-s ps-3"
          aria-label={`${name}'s keys`}
        >
          {keys.map((key) => (
            <KeyRow key={key.name} provider={provider} keyed={key} />
          ))}
        </ul>
      )}
    </div>
  )
}

// TinyFishNote says what TinyFish's terms allow (web-search-plus keeps TinyFish explicit-only for it,
// providers.d/tinyfish.py); it is on by default here (owner, 2026-11-14).
function TinyFishNote() {
  return (
    <p className="ms-6 mt-1.5 flex items-start gap-1.5 ps-3 text-xs text-muted-foreground">
      <Info className="mt-0.5 size-3 shrink-0" aria-hidden />
      <span>
        TinyFish&apos;s terms let it use your queries to train its models.{" "}
        <a
          href="https://www.tinyfish.ai/terms"
          target="_blank"
          rel="noreferrer"
          className="underline underline-offset-4"
        >
          Its terms
        </a>
        . Switch it off to keep queries away from it.
      </span>
    </p>
  )
}

// KeyRow is one key: its name in the vault, its state, when it was last used, what it used, and its removal.
function KeyRow({ provider, keyed }: { provider: WebSearchKeyedProvider; keyed: WebSearchKey }) {
  const queryClient = useQueryClient()
  const state = keyState(keyed)
  const share = balanceShare(keyed)
  const spend = spendText(keyed.usage)

  return (
    <li
      aria-label={keyed.name}
      className="flex flex-col gap-1.5 rounded-md border px-3 py-2 text-xs sm:flex-row sm:items-center sm:gap-3"
    >
      <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
        <span className="truncate font-mono text-foreground">{keyed.name}</span>
        <StateBadge state={state} icon={keyed.state === "ready" ? undefined : Timer} />
        {keyed.last_used_at && <Fact icon={Clock}>Used {since(keyed.last_used_at)}</Fact>}
      </span>
      <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground">
        <Usage usage={keyed.usage} />
        {spend && <Fact icon={CircleDollarSign}>{spend}</Fact>}
        {share !== undefined ? (
          <span className="flex w-40 items-center gap-2" title={balanceText(keyed)}>
            <Meter label={`${keyed.name}'s balance used`} value={share} connected />
          </span>
        ) : (
          <Unknown />
        )}
        {share !== undefined && <span className="whitespace-nowrap">{balanceText(keyed)}</span>}
      </span>
      <span className="self-end sm:self-auto">
        <ConfirmDialog
          trigger={
            <Button variant="ghost" size="sm" aria-label={`Remove ${keyed.name}`}>
              <Trash2 data-icon="inline-start" />
              Remove
            </Button>
          }
          title={`Remove ${keyed.name}?`}
          consequence={`It is deleted from the vault and ${providerName(provider)} is no longer asked with it.`}
          action="Remove"
          run={async () =>
            queryClient.setQueryData(
              webSearchKey,
              (await removeWebSearchKey({ path: { provider, name: keyed.name } })).data,
            )
          }
          onDone={() => undefined}
          done={`${keyed.name} was removed`}
        />
      </span>
    </li>
  )
}

// Unknown says that nothing is known of the allowance, and why, when pointed at (search-rotation's "unknown
// allowance": no cap is invented).
function Unknown() {
  return (
    <Tooltip>
      <TooltipTrigger
        render={<span className="cursor-help whitespace-nowrap underline decoration-dotted" />}
      >
        Allowance unknown
      </TooltipTrigger>
      <TooltipContent>
        <span className="block max-w-64 text-pretty">
          The provider tells no balance. The counts are this control plane&apos;s own calls, by UTC
          day and month.
        </span>
      </TooltipContent>
    </Tooltip>
  )
}

const keySchema = z.object({ key: z.string().trim().min(1, "Paste the key.") })

// AddKeyDialog takes one more key of a provider into the vault: a password field, never filled with a key kept.
function AddKeyDialog({ provider, first }: { provider: WebSearchKeyedProvider; first: boolean }) {
  const formId = useId()
  const [open, setOpen] = useState(false)
  const name = providerName(provider)
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: async (key: string) =>
      (await addWebSearchKey({ path: { provider }, body: { key } })).data,
    onSuccess: (added) => {
      queryClient.setQueryData(webSearchKey, added.web_search)
      said(`${name}'s key is kept in the vault as ${added.name}`)
    },
  })
  const form = useForm({
    defaultValues: { key: "" },
    validators: { onSubmit: keySchema },
    onSubmit: async ({ value }) => {
      await save.mutateAsync(value.key.trim())
      setOpen(false)
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) {
          form.reset()
          save.reset()
        }
      }}
    >
      <DialogTrigger
        render={
          <Button variant={first ? "outline" : "ghost"} size="sm" aria-label={`Add a ${name} key`}>
            {first ? <KeyRound data-icon="inline-start" /> : <Plus data-icon="inline-start" />}
            Add key
          </Button>
        }
      />
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>{name} API key</DialogTitle>
          <DialogDescription>
            {first
              ? `With a key, ${name} is asked through its own API, before the providers without a key.`
              : `One more key of your own ${name} account; the keys are used in turn.`}{" "}
            Get one at{" "}
            <a
              href={keyPages[provider]}
              target="_blank"
              rel="noreferrer"
              className="underline underline-offset-4"
            >
              {new URL(keyPages[provider]).host}
            </a>
            .
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            autoComplete="off"
            noValidate
            onSubmit={(event) => {
              event.preventDefault()
              void form.handleSubmit()
            }}
          >
            <FieldGroup>
              <form.Field name="key">
                {(field) => (
                  <TextField
                    field={field}
                    label="API key"
                    secret
                    placeholder="Paste the key"
                    description="It is kept in the vault and never shown again."
                  />
                )}
              </form.Field>
            </FieldGroup>
          </form>
        </DialogPanel>
        <ErrorAlert error={save.error} />
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={save.isPending}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            Save key
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// LinkRow is a keyless provider or a subscription: whose it is, where its search comes from, its state, what it
// used, and what the owner can do with it.
function LinkRow({ link }: { link: WebSearchLink }) {
  const name = providerName(link.provider)
  const state = linkState(link)
  const toggle = useSwitch(link)

  return (
    <div className={link.on ? undefined : "opacity-70"}>
      <Item variant="muted" size="sm" aria-label={`${name}, ${sourceText(link)}`}>
        <Mark provider={link.provider} />
        <ItemContent>
          <ItemTitle>{name}</ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <StateBadge state={state} icon={stateIcon(link.state)} />
              <Fact icon={link.source === "public" ? Globe : UserRound}>{sourceText(link)}</Fact>
              {link.source === "public" && (
                <>
                  <Usage usage={link.usage} />
                  <Unknown />
                </>
              )}
            </span>
          </ItemDescription>
          <ErrorAlert error={toggle.error} />
        </ItemContent>
        <div className="self-start">
          <ItemActions>
            {link.source === "subscription" && link.state === "not_set_up" && (
              <Link
                to="/settings/models"
                className={buttonVariants({ variant: "ghost", size: "sm" })}
              >
                <Plus data-icon="inline-start" />
                Add an account
              </Link>
            )}
            <InUse link={link} toggle={toggle} />
          </ItemActions>
        </div>
      </Item>
    </div>
  )
}

// InUse is the owner's switch of a provider; its ways with keys and without share it.
function InUse({ link, toggle }: { link: WebSearchLink; toggle: ReturnType<typeof useSwitch> }) {
  const id = `use-${link.provider}-${link.source}`
  const shared = isKeyed(link.provider) && link.provider !== "tinyfish"

  return (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <label
        htmlFor={id}
        className="inline-flex items-center gap-1"
        title={
          shared
            ? `${providerName(link.provider)} with your keys and without a key share one switch`
            : undefined
        }
      >
        <Power className="size-3" aria-hidden />
        In use
      </label>
      <Switch
        id={id}
        size="sm"
        checked={link.on}
        disabled={toggle.isPending}
        onCheckedChange={(next) => toggle.mutate(next)}
      />
    </span>
  )
}

// Usage is this control plane's calls today and this month.
function Usage({ usage }: { usage: WebSearchLink["usage"] }) {
  return <span className="whitespace-nowrap tabular-nums">{callsText(usage)}</span>
}

function StateBadge({
  state,
  icon: Icon,
}: {
  state: ReturnType<typeof linkState>
  icon?: LucideIcon
}) {
  return (
    <Badge variant={state.tone} title={state.why}>
      {Icon && <Icon aria-hidden />}
      {state.label}
    </Badge>
  )
}

// Mark is the maker's mark of a provider, in its colours: Exa's, Tavily's and Firecrawl's as LobeHub's colour icons
// draw them, Parallel's, TinyFish's and You.com's as their websites serve them, and the subscriptions' as the Models
// page draws them.
function Mark({ provider }: { provider: WebSearchLink["provider"] }) {
  const className = "size-5 shrink-0 self-start"

  switch (provider) {
    case "exa":
      return <ExaIcon className={className} />
    case "tavily":
      return <TavilyIcon className={className} />
    case "firecrawl":
      return <FirecrawlIcon className={className} />
    case "parallel":
      return <ParallelIcon className={`${className} text-foreground`} />
    case "tinyfish":
      return <TinyFishIcon className={className} />
    case "you":
      return <YouIcon className={className} />
    case "codex":
    case "claude":
    case "antigravity":
      return <ProviderMark kind={`${provider}_subscription`} className={className} />
    default:
      return <Globe className={`${className} text-muted-foreground`} aria-hidden />
  }
}

// stateIcon says what kind of state a badge is, as the Models page's badges do.
function stateIcon(state: WebSearchLink["state"]): LucideIcon | undefined {
  if (state === "resting") return Timer
  if (state === "off") return Power

  return undefined
}

function Fact({ icon: Icon, children }: { icon: LucideIcon; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <Icon className="size-3" aria-hidden />
      {children}
    </span>
  )
}
