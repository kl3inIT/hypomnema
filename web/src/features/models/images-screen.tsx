import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  Box,
  GripVertical,
  ImageIcon,
  Pencil,
  Plus,
  Power,
  RotateCcw,
  Timer,
  UserRound,
  type LucideIcon,
} from "lucide-react"
import { useId, useState, type DragEvent, type ReactNode } from "react"
import { z } from "zod"

import "@/api/client"
import { getImages, setImageOrder, setImageProvider } from "@/api/generated/sdk.gen"
import type {
  ImageProvider,
  ImageProviderChange,
  ImageOrder,
  ImageProviderLink,
  Images,
} from "@/api/generated/types.gen"
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
import { Item, ItemActions, ItemContent, ItemDescription, ItemTitle } from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { ProviderMark } from "@/features/agents/model-mark"
import { ErrorAlert, SettingsSection, TextField } from "@/features/settings/settings-parts"
import { SettingsPage } from "@/features/settings/settings-shell"
import { said } from "@/features/settings/use-integrations"

import {
  accountsText,
  imageProviderName,
  imageState,
  madeText,
  modelPattern,
  modelText,
} from "./images-state"
import { moved } from "./web-search-state"

const imagesKey = ["images"] as const

// ImagesScreen is Settings → Gateway → Images (issue #431): the subscriptions that make and edit images, in the
// order a request asks them, as the Web search page lists its providers (web-search-screen.tsx) and OmniRoute and
// 9router keep their image providers beside their models. A provider is dragged by its handle on a desktop, as the
// Web search page's keyed providers are (search-rotation's dashboard); on a phone the order is set with
// `hypomnema image order`. Each row has its state, the model it makes images with, which the owner may change to
// try a newer one (owner, 2026-11-14), its accounts and what it made, and its switch. The page does what
// `hypomnema image providers|order` does.
export function ImagesScreen() {
  const images = useQuery({
    queryKey: imagesKey,
    queryFn: async () => (await getImages()).data,
    // A rest ends on its own, so the list is read again now and then while the page is open.
    refetchInterval: 30_000,
  })

  return (
    <SettingsPage
      title="Images"
      description="How agents make and edit images: your subscriptions in the order a request asks them, the model each uses, and what each made."
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
            note="Asked in this order; drag a provider by its handle to change it. A request that names a model is sent to that model; otherwise each provider uses the model shown. Claude makes no images."
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
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: change,
    onSuccess: (images, input) => {
      queryClient.setQueryData(imagesKey, images)
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
              <Fact icon={Box} title="The model it makes images with when a request names none">
                <span className="font-mono">{modelText(link)}</span>
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
            <ModelDialog link={link} />
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

const modelSchema = z.object({
  model: z
    .string()
    .trim()
    .refine((m) => m === "" || modelPattern.test(m), "Letters, digits and . _ : - only."),
})

// ModelDialog sets the model a provider makes images with: any name, passed on as it is, so a model released
// later can be tried; empty is the default again. The references' names are offered to pick.
function ModelDialog({ link }: { link: ImageProviderLink }) {
  const formId = useId()
  const [open, setOpen] = useState(false)
  const name = imageProviderName(link.provider)
  const save = useProviderChange(link, (change) =>
    change.model
      ? `${name} makes images with ${change.model}`
      : `${name} makes images with its default model`,
  )
  const form = useForm({
    defaultValues: { model: link.model_chosen ? link.model : "" },
    validators: { onSubmit: modelSchema },
    onSubmit: async ({ value }) => {
      await save.mutateAsync({ model: value.model.trim() })
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
          <Button variant="ghost" size="sm" aria-label={`Change ${name}'s image model`}>
            <Pencil data-icon="inline-start" />
            Model
          </Button>
        }
      />
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>{name}&apos;s image model</DialogTitle>
          <DialogDescription>
            The model {name} makes images with when a request names none. Any name is passed to{" "}
            {name} as it is, so a newer model can be tried here; if {name} does not know it, the
            request fails with its words.
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
              <form.Field name="model">
                {(field) => (
                  <div className="flex flex-col gap-2">
                    <TextField
                      field={field}
                      label="Model"
                      placeholder={link.default_model}
                      description={`Empty is the default, ${link.default_model}.`}
                      action={
                        field.state.value !== "" ? (
                          <Button
                            type="button"
                            variant="ghost"
                            size="xs"
                            aria-label="Use the default model"
                            onClick={() => field.handleChange("")}
                          >
                            <RotateCcw data-icon="inline-start" />
                            Default
                          </Button>
                        ) : undefined
                      }
                    />
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-xs text-muted-foreground">Known names:</span>
                      {link.suggested_models.map((model) => (
                        <Button
                          key={model}
                          type="button"
                          variant="outline"
                          size="xs"
                          onClick={() =>
                            field.handleChange(model === link.default_model ? "" : model)
                          }
                        >
                          <span className="font-mono">{model}</span>
                        </Button>
                      ))}
                    </div>
                  </div>
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
            Save model
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
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
