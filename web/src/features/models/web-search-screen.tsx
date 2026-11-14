import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  Globe,
  KeyRound,
  ListOrdered,
  Pencil,
  Plus,
  Power,
  Server,
  Timer,
  Trash2,
  UserRound,
  type LucideIcon,
} from "lucide-react"
import { useId, useState, type ReactNode } from "react"
import { z } from "zod"

import "@/api/client"
import {
  deleteWebSearchExaKey,
  deleteWebSearchSearXng,
  getWebSearch,
  putWebSearchExaKey,
  putWebSearchSearXng,
  setWebSearchProvider,
} from "@/api/generated/sdk.gen"
import type { WebSearch, WebSearchLink } from "@/api/generated/types.gen"
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
import { ExaIcon, SearXNGIcon } from "@/components/icons/brand-icons"
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
import { ProviderMark } from "@/features/agents/model-mark"
import {
  ConfirmDialog,
  ErrorAlert,
  SettingsSection,
  TextField,
} from "@/features/settings/settings-parts"
import { SettingsPage } from "@/features/settings/settings-shell"
import { said } from "@/features/settings/use-integrations"

import { linkState, providerName, shareSwitch, sourceText } from "./web-search-state"

const webSearchKey = ["web-search"] as const

// WebSearchScreen is Settings → Gateway → Web search (issue #354): the search chain in the order a search tries it,
// each way with its maker's mark, where its search comes from, its state and a switch, as OmniRoute's search
// providers have a card each with a status and a switch (providers/components/ProviderCard.tsx) and 9router's web
// providers a status badge each (media-providers/web/page.js). The order is shown as 9router numbers a chain's
// providers (media-providers/combo/[id]/page.js, "Tried in order"); it is fixed (issue #354), so there are no
// arrows. Exa's key and SearXNG's address are entered in a dialog from their row, as OmniRoute's and 9router's
// "Add API Key" modals take a provider's key in a password field (OpenClaw shows a stored one as dots). A
// subscription's account is added on the Models page only; its row links there. The rows follow the Models
// page's account rows (models-screen.tsx). The page does what `hypomnema web providers ...` does.
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
      description="How agents search the web: the providers in the order a search tries them."
    >
      <ErrorAlert error={chain.error} />
      {chain.isPending ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : (
        chain.data && (
          <SettingsSection
            id="search-order"
            icon={ListOrdered}
            title="Search order"
            note="A search goes down this list until a provider answers. One that answered 429 rests until its time; one that is off is skipped."
          >
            <ItemGroup>
              {chain.data.chain.map((link, index) => (
                <LinkRow
                  key={`${link.provider}-${link.source}`}
                  link={link}
                  order={index + 1}
                  web={chain.data}
                />
              ))}
            </ItemGroup>
          </SettingsSection>
        )
      )}
    </SettingsPage>
  )
}

// useChange runs a change of the chain and shows the chain the server answers with.
function useChange<T>(
  change: (input: T) => Promise<{ data: WebSearch }>,
  done: (input: T) => string,
) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (input: T) => (await change(input)).data,
    onSuccess: (web, input) => {
      queryClient.setQueryData(webSearchKey, web)
      said(done(input))
    },
  })
}

// LinkRow is one way of asking: where it stands, whose it is, where its search comes from, its state, and what the
// owner can do with it.
function LinkRow({ link, order, web }: { link: WebSearchLink; order: number; web: WebSearch }) {
  const name = providerName(link.provider)
  const state = linkState(link)
  const shared = shareSwitch(web.chain, link)
  const id = `use-${link.provider}-${link.source}`
  const toggle = useChange(
    (on: boolean) => setWebSearchProvider({ path: { provider: link.provider }, body: { on } }),
    (on) => (on ? `${name} is on` : `${name} is off`),
  )

  return (
    <div className={link.on ? undefined : "opacity-70"}>
      <Item variant="muted" size="sm" aria-label={`${name}, ${sourceText(link)}`}>
        <span
          className="self-start pt-0.5 text-xs text-muted-foreground tabular-nums"
          title="A search tries the providers in this order"
        >
          #{order}
        </span>
        <Mark link={link} />
        <ItemContent>
          <ItemTitle>{name}</ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <Badge variant={state.tone} title={state.why}>
                <StateIcon state={link.state} />
                {state.label}
              </Badge>
              <Fact icon={sourceIcon(link)}>{sourceText(link)}</Fact>
              {link.source === "key" && link.state !== "not_set_up" && (
                <Fact icon={KeyRound}>In the vault as {web.exa_secret}</Fact>
              )}
              {link.source === "self_hosted" && web.searxng_url && (
                <span className="truncate font-mono">{web.searxng_url}</span>
              )}
            </span>
          </ItemDescription>
          <ErrorAlert error={toggle.error} />
        </ItemContent>
        <div className="self-start">
          <ItemActions>
            <RowActions link={link} web={web} />
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <label
                htmlFor={id}
                className="inline-flex items-center gap-1"
                title={shared ? `${name}'s two ways share one switch` : undefined}
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
          </ItemActions>
        </div>
      </Item>
    </div>
  )
}

// RowActions are what a row needs to be set up: Exa's key, SearXNG's address, or a subscription's account.
function RowActions({ link, web }: { link: WebSearchLink; web: WebSearch }) {
  const set = link.state !== "not_set_up"

  switch (link.source) {
    case "key":
      return set ? (
        <>
          <ExaKeyDialog replacing />
          <RemoveExaKey />
        </>
      ) : (
        <ExaKeyDialog />
      )
    case "self_hosted":
      return web.searxng_url ? (
        <>
          <SearXNGDialog address={web.searxng_url} />
          <RemoveSearXNG />
        </>
      ) : (
        <SearXNGDialog />
      )
    case "subscription":
      return set ? null : (
        <Link to="/settings/models" className={buttonVariants({ variant: "ghost", size: "sm" })}>
          <Plus data-icon="inline-start" />
          Add an account
        </Link>
      )
    default:
      return null
  }
}

const keySchema = z.object({ key: z.string().trim().min(1, "Paste the key.") })

// ExaKeyDialog takes Exa's API key into the vault: a password field, never filled with the key kept.
function ExaKeyDialog({ replacing }: { replacing?: boolean }) {
  const formId = useId()
  const [open, setOpen] = useState(false)
  const save = useChange(
    (key: string) => putWebSearchExaKey({ body: { key } }),
    () => "Exa's key is kept in the vault",
  )
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
          <Button variant={replacing ? "ghost" : "outline"} size="sm">
            {replacing ? (
              <Pencil data-icon="inline-start" />
            ) : (
              <KeyRound data-icon="inline-start" />
            )}
            {replacing ? "Replace key" : "Add key"}
          </Button>
        }
      />
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>Exa API key</DialogTitle>
          <DialogDescription>
            With a key, Exa is asked first, through its own API. Get one at{" "}
            <a
              href="https://dashboard.exa.ai/api-keys"
              target="_blank"
              rel="noreferrer"
              className="underline underline-offset-4"
            >
              dashboard.exa.ai
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
                    placeholder={replacing ? "••••••••" : "exa-…"}
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

function RemoveExaKey() {
  const queryClient = useQueryClient()

  return (
    <ConfirmDialog
      trigger={
        <Button variant="ghost" size="sm" aria-label="Remove Exa's key">
          <Trash2 data-icon="inline-start" />
          Remove
        </Button>
      }
      title="Remove Exa's API key?"
      consequence="It is deleted from the vault. Exa is then asked through its public MCP, with no key, which has a lower limit."
      action="Remove"
      run={async () => queryClient.setQueryData(webSearchKey, (await deleteWebSearchExaKey()).data)}
      onDone={() => undefined}
      done="Exa's key was removed"
    />
  )
}

const addressSchema = z.object({
  url: z
    .string()
    .trim()
    .regex(/^https?:\/\/[^/\s]+/, "An address that starts with http:// or https://."),
})

// SearXNGDialog takes the address of the owner's SearXNG, as OmniRoute's and OpenClaw's base URL field.
function SearXNGDialog({ address }: { address?: string }) {
  const formId = useId()
  const [open, setOpen] = useState(false)
  const save = useChange(
    (url: string) => putWebSearchSearXng({ body: { url } }),
    () => "SearXNG's address is set",
  )
  const form = useForm({
    defaultValues: { url: address ?? "" },
    validators: { onSubmit: addressSchema },
    onSubmit: async ({ value }) => {
      await save.mutateAsync(value.url.trim())
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
          <Button variant={address ? "ghost" : "outline"} size="sm">
            {address ? <Pencil data-icon="inline-start" /> : <Server data-icon="inline-start" />}
            {address ? "Change" : "Set address"}
          </Button>
        }
      />
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>SearXNG address</DialogTitle>
          <DialogDescription>
            Your own SearXNG, self-hosted meta-search with no API key. It is asked after Exa with
            its key.
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
              <form.Field name="url">
                {(field) => (
                  <TextField
                    field={field}
                    label="Base URL"
                    placeholder="http://localhost:8888"
                    description="It may be on your local network. /search is added when it is missing."
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
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function RemoveSearXNG() {
  const queryClient = useQueryClient()

  return (
    <ConfirmDialog
      trigger={
        <Button variant="ghost" size="sm" aria-label="Forget SearXNG's address">
          <Trash2 data-icon="inline-start" />
          Remove
        </Button>
      }
      title="Forget SearXNG's address?"
      consequence="Searches no longer ask your SearXNG. The SearXNG itself is not touched."
      action="Remove"
      run={async () =>
        queryClient.setQueryData(webSearchKey, (await deleteWebSearchSearXng()).data)
      }
      onDone={() => undefined}
      done="SearXNG's address was forgotten"
    />
  )
}

// Mark is the maker's mark of a way: Exa's and SearXNG's, the subscriptions' as the Models page draws them, and a
// globe for Parallel, whose mark the references carry only as a picture.
function Mark({ link }: { link: WebSearchLink }) {
  const className = "size-5 shrink-0 self-start"

  switch (link.provider) {
    case "exa":
      return <ExaIcon className={className} />
    case "searxng":
      return <SearXNGIcon className={className} />
    case "parallel":
      return <Globe className={`${className} text-muted-foreground`} aria-hidden />
    default:
      return <ProviderMark kind={`${link.provider}_subscription`} className={className} />
  }
}

function sourceIcon(link: WebSearchLink): LucideIcon {
  switch (link.source) {
    case "key":
      return KeyRound
    case "self_hosted":
      return Server
    case "public":
      return Globe
    case "subscription":
      return UserRound
  }
}

// StateIcon says what kind of state a badge is, as the Models page's badges do.
function StateIcon({ state }: { state: WebSearchLink["state"] }) {
  if (state === "resting") return <Timer aria-hidden />
  if (state === "off") return <Power aria-hidden />

  return null
}

function Fact({ icon: Icon, children }: { icon: LucideIcon; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <Icon className="size-3" aria-hidden />
      {children}
    </span>
  )
}
