import { useMutation } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  Box,
  GripVertical,
  ImageIcon,
  Plus,
  Power,
  Timer,
  UserRound,
  type LucideIcon,
} from "lucide-react"
import { useState, type DragEvent, type ReactNode } from "react"

import "@/api/client"
import { setImageOrder, setImageProvider } from "@/api/generated/sdk.gen"
import type {
  ImageProvider,
  ImageProviderChange,
  ImageOrder,
  ImageProviderLink,
  Images,
} from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { buttonVariants } from "@/components/ui/button"
import { Item, ItemActions, ItemContent, ItemDescription, ItemTitle } from "@/components/ui/item"
import { Switch } from "@/components/ui/switch"
import { ProviderMark } from "@/components/icons/model-mark"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsSection, SettingsPage } from "@/components/settings-page"
import { said } from "@/lib/said"

import { accountsText, imageProviderName, imageState, madeText } from "./images-state"
import { useImages, useShowImages } from "./use-models"
import { moved } from "./web-search-state"

// ImagesScreen is Settings → Gateway → Images (issue #431): the subscriptions that make and edit images, in the
// order a request asks them, as the Web search page lists its providers (web-search-screen.tsx) and OmniRoute and
// 9router keep their image providers beside their models. A provider is dragged by its handle on a desktop, as the
// Web search page's keyed providers are (search-rotation's dashboard); on a phone the order is set with
// `hypomnema image order`. Each row has its state, the one model it makes images with (issue #453: not chosen),
// named as OmniRoute names it with its id beside, its accounts and what it made, and its switch. The page does
// what `hypomnema image providers|order` does.
export function ImagesScreen() {
  const images = useImages()

  return (
    <SettingsPage
      title="Images"
      description="How agents make and edit images: your subscriptions in the order a request asks them, the model each makes them with, and what each made."
    >
      <ErrorAlert error={images.error} />
      {images.isPending ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : (
        images.data && (
          <SettingsSection
            id="images-providers"
            icon={UserRound}
            title="From your subscriptions"
            note="Asked in this order; drag a provider by its handle to change it. Each provider makes images with the model shown; a request that names a model only picks the provider. Claude makes no images."
          >
            <ProviderList links={images.data.providers} />
          </SettingsSection>
        )
      )}
    </SettingsPage>
  )
}

// useChange runs a change and shows the providers the server answers with.
function useChange<T>(change: (input: T) => Promise<Images>, done: (input: T) => string) {
  const show = useShowImages()

  return useMutation({
    mutationFn: change,
    onSuccess: (images, input) => {
      show(images)
      said(done(input))
    },
  })
}

function useProviderChange(link: ImageProviderLink, done: (change: ImageProviderChange) => string) {
  return useChange(
    async (change: ImageProviderChange) =>
      (await setImageProvider({ path: { provider: link.provider }, body: change })).data,
    done,
  )
}

// ProviderList is the providers in their order. One is dragged by its handle and dropped on another's place; the
// new order is sent at once and the server's answer shown.
function ProviderList({ links }: { links: ImageProviderLink[] }) {
  const [dragged, setDragged] = useState<ImageProvider | null>(null)
  const [over, setOver] = useState<ImageProvider | null>(null)
  const order = links.map((l) => l.provider)
  const reorder = useChange(
    async (providers: ImageProvider[]) =>
      (await setImageOrder({ body: { providers } as ImageOrder })).data,
    (providers) =>
      providers[0] ? `${imageProviderName(providers[0])} is asked first` : "The order is kept",
  )

  const drop = (to: ImageProvider) => {
    const from = dragged
    setDragged(null)
    setOver(null)

    if (from && from !== to) reorder.mutate(moved(order, from, to))
  }

  return (
    <div className="flex flex-col gap-2.5">
      <ErrorAlert error={reorder.error} />
      {links.map((link, index) => (
        <div
          key={link.provider}
          data-dragging={dragged === link.provider || undefined}
          data-over={(over === link.provider && dragged !== link.provider) || undefined}
          className="rounded-lg outline-offset-2 data-dragging:opacity-50 data-over:outline-2 data-over:outline-ring"
          onDragOver={(event: DragEvent) => {
            if (!dragged) return
            event.preventDefault()
            setOver(link.provider)
          }}
          onDragLeave={() => setOver((now) => (now === link.provider ? null : now))}
          onDrop={(event: DragEvent) => {
            event.preventDefault()
            drop(link.provider)
          }}
        >
          <ProviderRow
            link={link}
            order={index + 1}
            busy={reorder.isPending}
            onDragStart={() => setDragged(link.provider)}
            onDragEnd={() => {
              setDragged(null)
              setOver(null)
            }}
          />
        </div>
      ))}
    </div>
  )
}

// ProviderRow is one provider: its place, its mark, its state, its model, its accounts, what it made, and what the
// owner can do with it.
function ProviderRow({
  link,
  order,
  busy,
  onDragStart,
  onDragEnd,
}: {
  link: ImageProviderLink
  order: number
  busy: boolean
  onDragStart: () => void
  onDragEnd: () => void
}) {
  const name = imageProviderName(link.provider)
  const state = imageState(link)
  const toggle = useProviderChange(link, (change) =>
    change.on ? `${name} is on` : `${name} is off`,
  )

  return (
    <div className={link.on ? undefined : "opacity-70"}>
      <Item variant="muted" size="sm" aria-label={name}>
        {/* The handle is its own design: a grip that is dragged, not pressed (search-rotation's .handle). */}
        <span
          draggable={!busy}
          onDragStart={(event) => {
            event.dataTransfer.effectAllowed = "move"
            event.dataTransfer.setData("text/plain", link.provider)
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
          title="A request asks the providers in this order"
        >
          #{order}
        </span>
        <ProviderMark
          kind={`${link.provider}_subscription`}
          className="size-5 shrink-0 self-start"
        />
        <ItemContent>
          <ItemTitle>{name}</ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <StateBadge state={state} icon={stateIcon(link.state)} />
              <Fact icon={Box} title="The model it makes images with">
                {link.model_name}
                <span className="font-mono text-muted-foreground/80">· {link.model}</span>
              </Fact>
              <Fact icon={UserRound}>{accountsText(link)}</Fact>
              <Fact icon={ImageIcon} title="This control plane's requests, by UTC day and month">
                <span className="tabular-nums">{madeText(link.usage)}</span>
              </Fact>
            </span>
          </ItemDescription>
          <ErrorAlert error={toggle.error} />
        </ItemContent>
        <div className="self-start">
          <ItemActions>
            {link.state === "not_set_up" && (
              <Link
                to="/settings/models"
                className={buttonVariants({ variant: "ghost", size: "sm" })}
              >
                <Plus data-icon="inline-start" />
                Add an account
              </Link>
            )}
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <label htmlFor={`use-${link.provider}`} className="inline-flex items-center gap-1">
                <Power className="size-3" aria-hidden />
                In use
              </label>
              <Switch
                id={`use-${link.provider}`}
                size="sm"
                checked={link.on}
                disabled={toggle.isPending}
                onCheckedChange={(on) => toggle.mutate({ on })}
              />
            </span>
          </ItemActions>
        </div>
      </Item>
    </div>
  )
}

function StateBadge({
  state,
  icon: Icon,
}: {
  state: ReturnType<typeof imageState>
  icon?: LucideIcon
}) {
  return (
    <Badge variant={state.tone} title={state.why}>
      {Icon && <Icon aria-hidden />}
      {state.label}
    </Badge>
  )
}

function stateIcon(state: ImageProviderLink["state"]): LucideIcon | undefined {
  if (state === "resting") return Timer
  if (state === "off") return Power

  return undefined
}

function Fact({
  icon: Icon,
  title,
  children,
}: {
  icon: LucideIcon
  title?: string
  children: ReactNode
}) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap" title={title}>
      <Icon className="size-3" aria-hidden />
      {children}
    </span>
  )
}
