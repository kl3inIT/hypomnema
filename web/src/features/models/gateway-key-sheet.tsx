import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  ArrowLeft,
  ArrowRight,
  Cable,
  Check,
  ChevronRight,
  Copy,
  Pencil,
  Plus,
  X,
} from "lucide-react"
import { useId, useState, type ReactNode } from "react"
import { z } from "zod"

import "@/api/client"
import {
  createGatewayKeyMutation,
  updateGatewayKeyMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import type { CreatedGatewayKey, GatewayKey, WebSearchProvider } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/components/ui/code-block"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import {
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet"
import { Spinner } from "@/components/ui/spinner"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { ProviderMark } from "@/components/icons/model-mark"
import { SearchProviderMark } from "@/components/icons/provider-marks"
import { ErrorAlert } from "@/components/error-alert"
import { TextField } from "@/components/text-field"
import { said } from "@/lib/said"

import {
  connectSnippet,
  endpointKinds,
  pickGroups,
  routingBody,
  routingOf,
  routingSummary,
  type Routing,
} from "./gateway-keys-state"
import { GatewayModelPicker } from "./gateway-model-picker"
import { subscriptionsOf } from "./models-state"
import { useAccounts, useGatewayModels, useProviders, useRefreshGatewayKeys } from "./use-models"
import { providerName } from "./web-search-state"

// The name the gateway gives to "every model".
const every = "*"

const endpointPaths =
  "Search: /v1/search and the Codex CLI's own · Pages: /v1/web/fetch · Images: /v1/images/* · Speech: /v1/audio/*"

// Every web search provider, in the order Settings lists them first.
const searchProviders = [
  "exa",
  "firecrawl",
  "parallel",
  "tavily",
  "tinyfish",
  "you",
  "codex",
  "claude",
  "antigravity",
] as const satisfies readonly WebSearchProvider[]

const keySchema = z
  .object({
    name: z
      .string()
      .regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Lower-case letters, digits and dashes, 63 at most."),
    scope: z.enum(["every", "some"]),
    models: z.array(z.string()),
    endpoints: z.array(z.enum(["search", "fetch", "images", "audio"])),
    routing: z.object({
      accounts: z.enum(["every", "some"]),
      chosen: z.array(z.string()),
      order: z.enum(["settings", "own"]),
      providers: z.array(z.enum(searchProviders)),
    }),
  })
  .refine((value) => value.scope === "every" || value.models.length > 0, {
    message: "Choose a model, or every model.",
    path: ["models"],
  })
  .refine((value) => value.routing.accounts === "every" || value.routing.chosen.length > 0, {
    message: "Choose an account, or every account.",
    path: ["routing"],
  })
  .refine((value) => value.routing.order === "settings" || value.routing.providers.length > 0, {
    message: "Choose a provider to ask first, or the order of Settings.",
    path: ["routing"],
  })

// Segmented is a choice of two or three, the Machines page's segmented control (machines-screen.tsx, its layout).
function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: T
  options: { value: T; label: string }[]
  onChange: (next: T) => void
}) {
  return (
    <fieldset className="m-0 inline-flex w-fit gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
      <legend className="sr-only">{label}</legend>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          aria-pressed={value === option.value}
          onClick={() => onChange(option.value)}
          className="rounded-md px-2.5 py-1 text-xs text-muted-foreground aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
        >
          {option.label}
        </button>
      ))}
    </fieldset>
  )
}

// KeySheet makes a gateway key or edits one, in a sheet at the right side (issue #1277): Bifrost's virtual key sheet
// and new-api's key drawer are one form for both (virtualKeySheet.tsx, api-keys-mutate-drawer.tsx). The name comes
// first; what a key may use follows; how its requests are routed is a section closed by default, as new-api's
// Advanced Settings and LiteLLM's Router settings are, whose header says what is set. After a key is made the sheet
// shows it once, with what a tool needs to use it.
export function KeySheet({ gatewayKey }: { gatewayKey?: GatewayKey }) {
  const refresh = useRefreshGatewayKeys()
  const [open, setOpen] = useState(false)
  const [routingOpen, setRoutingOpen] = useState(false)
  const [made, setMade] = useState<CreatedGatewayKey | null>(null)
  const formId = useId()

  const providers = useProviders()
  const models = useGatewayModels({ enabled: open })
  const groups = pickGroups(models.data ?? [], providers.data ?? [])

  const create = useMutation({
    ...createGatewayKeyMutation(),
    gcTime: 0,
    onSuccess: async (created) => {
      setMade(created)
      await refresh()
    },
  })
  const update = useMutation({
    ...updateGatewayKeyMutation(),
    onSuccess: async (key) => {
      await refresh()
      said(`${key.name} was updated`)
      change(false)
    },
  })
  const pending = create.isPending || update.isPending

  const form = useForm({
    defaultValues: {
      name: gatewayKey?.name ?? "",
      scope: (gatewayKey && !gatewayKey.models.includes(every) ? "some" : "every") as
        | "every"
        | "some",
      models: gatewayKey?.models.filter((model) => model !== every) ?? [],
      endpoints: gatewayKey?.endpoints ?? endpointKinds.map((kind) => kind.id),
      routing: routingOf(gatewayKey),
    },
    validators: { onSubmit: keySchema },
    onSubmit: ({ value, formApi }) => {
      const access = { name: value.name, models: value.scope === "every" ? [] : value.models }
      const everyEndpoint = value.endpoints.length === endpointKinds.length
      const routing = routingBody(value.routing, Boolean(gatewayKey))
      if (gatewayKey) {
        const sameEndpoints =
          gatewayKey.endpoints.length === value.endpoints.length &&
          gatewayKey.endpoints.every((kind) => value.endpoints.includes(kind))
        update.mutate({
          path: { key_id: gatewayKey.id },
          body: {
            ...access,
            ...routing,
            ...((!sameEndpoints ||
              (everyEndpoint && formApi.getFieldMeta("endpoints")?.isDirty)) && {
              endpoints: everyEndpoint ? ["*"] : value.endpoints,
            }),
          },
        })
        return
      }
      create.mutate({
        body: {
          ...access,
          ...routing,
          // Every kind is sent as none named, so that a kind added later is given too.
          ...(!everyEndpoint && { endpoints: value.endpoints }),
        },
      })
    },
    onSubmitInvalid: ({ formApi }) => {
      // A choice of the Routing section that is not finished opens it, so that its error is seen.
      if (formApi.getFieldMeta("routing")?.errors.length) setRoutingOpen(true)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setMade(null)
      setRoutingOpen(false)
      form.reset()
      create.reset()
      update.reset()
    }
  }

  return (
    <Sheet open={open} onOpenChange={change}>
      <SheetTrigger
        render={
          gatewayKey ? (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`Edit ${gatewayKey.name}`}
              title="Edit key"
            >
              <Pencil />
            </Button>
          ) : (
            <Button>
              <Plus data-icon="inline-start" />
              Create key
            </Button>
          )
        }
      />
      <SheetContent size="lg">
        {made ? (
          <CreatedKey made={made} />
        ) : (
          <>
            <SheetHeader>
              <SheetTitle>{gatewayKey ? `Edit ${gatewayKey.name}` : "New gateway key"}</SheetTitle>
              <SheetDescription>
                {gatewayKey
                  ? "Change what the key can do without replacing the value used by your tool."
                  : "For one tool on one machine, so that it can be revoked alone."}
              </SheetDescription>
            </SheetHeader>
            <form
              id={formId}
              autoComplete="off"
              noValidate
              className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-4 pb-4"
              onSubmit={(event) => {
                event.preventDefault()
                void form.handleSubmit()
              }}
            >
              <FieldGroup>
                <form.Field name="name">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="opencode-laptop"
                      description="Lower-case letters, digits and dashes."
                    />
                  )}
                </form.Field>
                <FieldSet>
                  <FieldLegend variant="label">Models</FieldLegend>
                  <form.Field name="scope">
                    {(field) => (
                      <Segmented
                        label="Models"
                        value={field.state.value}
                        options={[
                          { value: "every", label: "Every model" },
                          { value: "some", label: "Only these" },
                        ]}
                        onChange={field.handleChange}
                      />
                    )}
                  </form.Field>
                  <form.Subscribe selector={(state) => state.values.scope}>
                    {(scope) =>
                      scope === "some" ? (
                        <form.Field name="models">
                          {(field) => (
                            <div className="flex flex-col gap-1.5">
                              {models.isPending ? (
                                <Spinner />
                              ) : (
                                <GatewayModelPicker
                                  groups={groups}
                                  chosen={field.state.value}
                                  onChange={field.handleChange}
                                  invalid={!field.state.meta.isValid}
                                />
                              )}
                              {!field.state.meta.isValid && (
                                <FieldError errors={field.state.meta.errors} />
                              )}
                            </div>
                          )}
                        </form.Field>
                      ) : (
                        <FieldDescription>Also the models added later.</FieldDescription>
                      )
                    }
                  </form.Subscribe>
                </FieldSet>
                <FieldSet>
                  <FieldLegend variant="label">Besides chat</FieldLegend>
                  <form.Field name="endpoints">
                    {(field) => (
                      <ToggleGroup
                        multiple
                        aria-label="Besides chat"
                        variant="choice"
                        size="sm"
                        spacing={1}
                        value={field.state.value}
                        onValueChange={(next: string[]) =>
                          field.handleChange(
                            endpointKinds.map((kind) => kind.id).filter((id) => next.includes(id)),
                          )
                        }
                      >
                        {endpointKinds.map((kind) => (
                          <ToggleGroupItem
                            key={kind.id}
                            value={kind.id}
                            aria-label={kind.title}
                            title={kind.title}
                          >
                            <kind.icon data-icon="inline-start" />
                            {kind.shortLabel}
                          </ToggleGroupItem>
                        ))}
                      </ToggleGroup>
                    )}
                  </form.Field>
                  <FieldDescription>{endpointPaths}</FieldDescription>
                </FieldSet>
                <form.Subscribe selector={(state) => state.values.routing}>
                  {(routing) => (
                    <Collapsible open={routingOpen} onOpenChange={setRoutingOpen}>
                      <div className="rounded-lg border">
                        <CollapsibleTrigger
                          render={
                            <button
                              type="button"
                              aria-label={`Routing: ${routingSummary(routing)}`}
                              className="flex w-full cursor-pointer items-center gap-2 rounded-lg px-3 py-2.5 text-start text-sm font-medium outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring"
                            />
                          }
                        >
                          <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform [[data-panel-open]_&]:rotate-90" />
                          Routing
                          <span
                            className="ms-auto truncate text-xs font-normal text-muted-foreground"
                            data-testid="routing-summary"
                          >
                            {routingSummary(routing)}
                          </span>
                        </CollapsibleTrigger>
                        <CollapsibleContent>
                          <form.Field name="routing">
                            {(field) => (
                              <div className="flex flex-col gap-5 border-t px-3 py-4">
                                <AccountsChoice
                                  routing={field.state.value}
                                  enabled={open}
                                  onChange={field.handleChange}
                                />
                                <SearchOrderChoice
                                  routing={field.state.value}
                                  onChange={field.handleChange}
                                />
                                {!field.state.meta.isValid && (
                                  <FieldError errors={field.state.meta.errors} />
                                )}
                              </div>
                            )}
                          </form.Field>
                        </CollapsibleContent>
                      </div>
                    </Collapsible>
                  )}
                </form.Subscribe>
              </FieldGroup>
            </form>
            <SheetFooter>
              <ErrorAlert error={create.error ?? update.error} />
              <div className="flex justify-end gap-2">
                <SheetClose render={<Button variant="outline" />}>Cancel</SheetClose>
                <Button
                  type="submit"
                  form={formId}
                  focusableWhenDisabled={pending}
                  disabled={pending}
                >
                  {pending && <Spinner data-icon="inline-start" />}
                  {gatewayKey ? "Save changes" : "Create key"}
                </Button>
              </div>
            </SheetFooter>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

// Section is one choice of the Routing section: its title, its choice and what it does.
function Section({ title, hint, children }: { title: string; hint: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">{title}</h3>
      {children}
      <p className="text-xs text-muted-foreground">{hint}</p>
    </section>
  )
}

// AccountsChoice limits a key to some accounts (issue #1277): every account, or only those chosen, one row a
// subscription with its accounts as chips, as OmniRoute's Connections tab chooses "All" or "Only selected"
// (KeyPolicyConnectionsTab.tsx) and Bifrost's provider card its keys.
function AccountsChoice({
  routing,
  enabled,
  onChange,
}: {
  routing: Routing
  enabled: boolean
  onChange: (next: Routing) => void
}) {
  const accounts = useAccounts()
  const providers = useProviders()
  const subscriptions = subscriptionsOf(providers.data ?? [], accounts.data ?? []).filter(
    (s) => s.accounts.length > 0,
  )

  return (
    <Section
      title="Accounts"
      hint="Only these: the key's requests go to the accounts chosen, in the order of Models among them. When they are all at their limit it waits, and no other account is used; a provider with none chosen is not used. An account added later is not added."
    >
      <Segmented
        label="Accounts"
        value={routing.accounts}
        options={[
          { value: "every", label: "Every account" },
          { value: "some", label: "Only these" },
        ]}
        onChange={(accounts) => onChange({ ...routing, accounts })}
      />
      {routing.accounts === "some" &&
        enabled &&
        (accounts.isPending || providers.isPending ? (
          <Spinner />
        ) : subscriptions.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No subscription account is signed in. Sign one in on Models.
          </p>
        ) : (
          <div className="flex flex-col gap-3">
            {subscriptions.map((subscription) => {
              const ids = subscription.accounts.map((account) => account.id)

              return (
                <div key={subscription.provider.name} className="flex flex-col gap-1.5">
                  <span className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                    <ProviderMark kind={subscription.provider.kind} className="size-3.5 shrink-0" />
                    {subscription.name}
                  </span>
                  <ToggleGroup
                    multiple
                    aria-label={`${subscription.name} accounts`}
                    variant="choice"
                    size="sm"
                    spacing={1}
                    value={routing.chosen.filter((id) => ids.includes(id))}
                    onValueChange={(next: string[]) =>
                      onChange({
                        ...routing,
                        chosen: [...routing.chosen.filter((id) => !ids.includes(id)), ...next],
                      })
                    }
                  >
                    {subscription.accounts.map((account) => (
                      <ToggleGroupItem key={account.id} value={account.id}>
                        {account.label}
                      </ToggleGroupItem>
                    ))}
                  </ToggleGroup>
                </div>
              )
            })}
          </div>
        ))}
    </Section>
  )
}

// SearchOrderChoice gives a key its own web search order (issue #1277): like Settings, or its own, the providers
// asked first as numbered chips that move and go, and the others to add, as OmniRoute's preferred connections are
// tried first and the next eligible after.
function SearchOrderChoice({
  routing,
  onChange,
}: {
  routing: Routing
  onChange: (next: Routing) => void
}) {
  const chosen = routing.providers
  const rest = searchProviders.filter((p) => !chosen.includes(p))

  function set(providers: WebSearchProvider[]) {
    onChange({ ...routing, providers })
  }

  function move(index: number, by: number) {
    const next = [...chosen]
    const [provider] = next.splice(index, 1)
    next.splice(index + by, 0, provider!)
    set(next)
  }

  return (
    <Section
      title="Web search order"
      hint="Its own: these are asked first, in this order; the others follow in the order of Settings. A provider off in Settings stays off, and Codex's and Claude's searches use the accounts above."
    >
      <Segmented
        label="Web search order"
        value={routing.order}
        options={[
          { value: "settings", label: "Like Settings" },
          { value: "own", label: "Its own" },
        ]}
        onChange={(order) => onChange({ ...routing, order })}
      />
      {routing.order === "own" && (
        <div className="flex flex-col gap-2">
          {chosen.length > 0 && (
            <ol aria-label="Asked first" className="flex flex-wrap gap-1.5">
              {chosen.map((provider, index) => (
                <li
                  key={provider}
                  className="flex h-7 items-center gap-1 rounded-full border bg-muted/40 ps-1 pe-0.5 text-xs font-medium"
                >
                  <span className="grid size-5 place-items-center rounded-full bg-foreground/9 text-[0.7rem] tabular-nums">
                    {index + 1}
                  </span>
                  <SearchProviderMark provider={provider} className="size-3.5 shrink-0" />
                  {providerName(provider)}
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`Move ${providerName(provider)} earlier`}
                    disabled={index === 0}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowLeft />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`Move ${providerName(provider)} later`}
                    disabled={index === chosen.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowRight />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`Remove ${providerName(provider)}`}
                    onClick={() => set(chosen.filter((p) => p !== provider))}
                  >
                    <X />
                  </Button>
                </li>
              ))}
            </ol>
          )}
          {rest.length > 0 && (
            <fieldset className="m-0 flex flex-wrap items-center gap-1.5 border-0 p-0">
              <legend className="sr-only">Add</legend>
              {rest.map((provider) => (
                <Button
                  key={provider}
                  type="button"
                  variant="outline"
                  size="chip"
                  aria-label={`Add ${providerName(provider)}`}
                  onClick={() => set([...chosen, provider])}
                >
                  <SearchProviderMark provider={provider} className="size-3.5 shrink-0" />
                  {providerName(provider)}
                </Button>
              ))}
            </fieldset>
          )}
        </div>
      )}
    </Section>
  )
}

// CreatedKey shows a new key once: its value with Copy, and what a tool needs to use it, as LiteLLM's created key
// gives its gateway URL and agent instructions (CreatedKeyDisplay.tsx) and OmniRoute shows a new key once; Connect a
// tool writes a tool's own files.
function CreatedKey({ made }: { made: CreatedGatewayKey }) {
  const origin = window.location.origin

  return (
    <>
      <SheetHeader>
        <SheetTitle>Copy the key of {made.key.name}</SheetTitle>
        <SheetDescription>
          Give this key to the tool as its API key.{" "}
          {made.key.masked_value
            ? "You can copy it again from the list."
            : "Copy it now: its value is not stored."}
        </SheetDescription>
      </SheetHeader>
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 pb-4">
        <InputGroup>
          <InputGroupInput readOnly aria-label="The new key" value={made.secret} />
          <InputGroupAddon align="inline-end">
            <CopyButton value={made.secret} label="Copy the key" done="The key was copied" />
          </InputGroupAddon>
        </InputGroup>
        <CodeBlock size="sm" aria-label="Connect a tool">
          <CodeBlockHeader>
            <CodeBlockTitle>
              <Cable />
              Connect a tool
            </CodeBlockTitle>
            <CodeBlockActions>
              <CodeBlockCopyButton value={connectSnippet(origin, made.secret)} />
            </CodeBlockActions>
          </CodeBlockHeader>
          <CodeBlockContent wrap>
            <span className="break-all">{connectSnippet(origin, made.secret)}</span>
          </CodeBlockContent>
        </CodeBlock>
        <p className="text-xs text-muted-foreground">
          Codex, omp and opencode read a file of their own:{" "}
          <Link
            to="/settings/connect"
            search={{ key: made.key.id }}
            className="font-medium text-foreground underline underline-offset-2"
          >
            Connect a tool
          </Link>{" "}
          writes it with this key.
        </p>
      </div>
      <SheetFooter>
        <div className="flex justify-end">
          <SheetClose render={<Button />}>Done</SheetClose>
        </div>
      </SheetFooter>
    </>
  )
}

export function CopyButton({ value, label, done }: { value: string; label: string; done: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      said(done)
    } catch {
      // No clipboard (an insecure page): the value stays selectable.
    }
  }

  return (
    <InputGroupButton size="icon-xs" aria-label={label} onClick={() => void copy()}>
      {copied ? <Check /> : <Copy />}
    </InputGroupButton>
  )
}
