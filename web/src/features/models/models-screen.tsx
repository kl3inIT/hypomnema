import { useMutation } from "@tanstack/react-query"
import {
  Bot,
  ArrowDown,
  ArrowUp,
  Box,
  CalendarDays,
  Crown,
  KeyRound,
  Link2,
  Mail,
  MonitorSmartphone,
  Plug,
  Plus,
  Power,
  RefreshCw,
  Route as RouteIcon,
  Tag,
  Timer,
  Trash2,
  TriangleAlert,
  Users,
  type LucideIcon,
} from "lucide-react"
import type { ReactNode } from "react"

import "@/api/client"
import { disableAccount, enableAccount, moveAccount, removeAccount } from "@/api/generated/sdk.gen"
import type { Account, Provider } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ModelMark, ProviderMark } from "@/features/agents/model-mark"
import { Meter } from "@/features/machines/machine-cells"
import { ConfirmDialog, ErrorAlert } from "@/features/settings/settings-parts"
import { SettingsPage } from "@/features/settings/settings-shell"
import { said } from "@/features/settings/use-integrations"
import { modelName } from "@/lib/model-name"
import { since } from "@/lib/time"

import { AccountSignInDialog, type SignInRuntime } from "./account-sign-in"
import {
  accountState,
  apiProvidersOf,
  inUse,
  routesOf,
  subscriptionState,
  subscriptionsOf,
  type Route,
  type Subscription,
  type Tone,
  until,
} from "./models-state"
import {
  useAccountLimits,
  useAccounts,
  useGatewayModels,
  useProviders,
  useRefreshAccounts,
  useRefreshLimits,
} from "./use-models"

// ModelsScreen is where the owner sees where models come from and looks after the accounts they are served
// from (issue #199). A subscription is a card with its accounts as rows, as an Integrations card has its
// accounts and as 9router's provider page (providers/[id]/ConnectionRow.js), OpenClaw's model providers
// (ui/src/pages/model-providers) and Orca's accounts pane (settings/accounts) have theirs. What the server
// cannot do yet is not here: signing an account in, changing the order, the limits, editing a route.
export function ModelsScreen() {
  const accounts = useAccounts()
  const providers = useProviders()
  const models = useGatewayModels()

  const subscriptions = subscriptionsOf(providers.data ?? [], accounts.data ?? [])
  const apis = apiProvidersOf(providers.data ?? [])
  const routes = routesOf(models.data ?? [], providers.data ?? [])
  const loading = accounts.isPending || providers.isPending || models.isPending

  return (
    <SettingsPage
      title="Models"
      description="Where models come from: the subscriptions and providers the gateway serves agents from."
    >
      <ErrorAlert error={accounts.error ?? providers.error ?? models.error} />
      {loading ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : (
        <>
          <Section
            id="subscriptions"
            icon={Users}
            title="Subscriptions"
            note="Accounts signed in on the control plane or on a machine. The gateway uses the first one in order that is in use and not resting."
          >
            {subscriptions.length === 0 ? (
              <Empty>
                No subscription is set up. Sign one in from a terminal: hypomnema accounts login
                claude
              </Empty>
            ) : (
              subscriptions.map((subscription) => (
                <SubscriptionCard key={subscription.provider.name} subscription={subscription} />
              ))
            )}
          </Section>

          {apis.length > 0 && (
            <Section
              id="api-providers"
              icon={Plug}
              title="API providers"
              note="Providers reached with a key from the Vault."
            >
              {apis.map((provider) => (
                <ApiProviderCard key={provider.name} provider={provider} />
              ))}
            </Section>
          )}

          <Section
            id="routes"
            icon={RouteIcon}
            title="Models"
            note="The names a client of the gateway asks for, and where each is served from."
          >
            {routes.length === 0 ? (
              <Empty>The gateway serves no model yet.</Empty>
            ) : (
              <Routes routes={routes} />
            )}
          </Section>
        </>
      )}
    </SettingsPage>
  )
}

function Section({
  id,
  icon: Icon,
  title,
  note,
  children,
}: {
  id: string
  icon: LucideIcon
  title: string
  note: string
  children: ReactNode
}) {
  return (
    <section aria-labelledby={id} className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <h2 id={id} className="flex items-center gap-2 text-sm font-semibold text-foreground">
          <Icon className="size-4 text-muted-foreground" aria-hidden />
          {title}
        </h2>
        <p className="text-xs text-muted-foreground">{note}</p>
      </div>
      {children}
    </section>
  )
}

function Empty({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-xl border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
      {children}
    </p>
  )
}

// SubscriptionCard is one subscription: its mark and name, whether it serves now, a switch for all its
// accounts at once (9router's card toggle, providers/page.js: it sets every connection of the provider), its
// accounts, and the models it offers.
function SubscriptionCard({ subscription }: { subscription: Subscription }) {
  const { provider, accounts } = subscription
  const refresh = useRefreshAccounts()
  const state = subscriptionState(accounts)
  const on = inUse(accounts)
  const id = `use-${provider.name}`

  const change = useMutation({
    mutationFn: async (next: boolean) => {
      // Only the accounts that are not as asked are changed.
      const others = accounts.filter((account) => (account.status === "active") !== next)

      await Promise.all(
        others.map((account) =>
          (next ? enableAccount : disableAccount)({ path: { account_id: account.id } }),
        ),
      )
    },
    onSettled: refresh,
    onSuccess: (_, next) =>
      said(
        next
          ? `${subscription.name}'s accounts are in use`
          : `${subscription.name}'s accounts are out of use`,
      ),
  })

  return (
    <Card size="sm" data-testid="subscription-card">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <ProviderMark kind={provider.kind} className="size-6 shrink-0" />
            <span className="flex flex-col gap-0.5">
              <span>{subscription.name}</span>
              <CardDescription>
                {subscription.account} · {count(accounts.length, "account")}
              </CardDescription>
            </span>
          </span>
        </CardTitle>
        <CardAction>
          <span className="flex flex-wrap items-center justify-end gap-2">
            <AccountSignInDialog
              runtime={subscription.runtime as SignInRuntime}
              name={subscription.name}
              trigger={
                <Button variant="outline" size="sm">
                  <Plus data-icon="inline-start" />
                  Add account
                </Button>
              }
            />
            {accounts.length > 0 && (
              <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <label htmlFor={id} className="inline-flex items-center gap-1">
                  <Power className="size-3" aria-hidden />
                  In use
                </label>
                <Switch
                  id={id}
                  size="sm"
                  checked={on}
                  disabled={change.isPending}
                  onCheckedChange={(next) => change.mutate(next)}
                />
              </span>
            )}
            <Badge variant={state.tone}>{state.label}</Badge>
          </span>
        </CardAction>
      </CardHeader>
      <CardContent>
        <div className="flex flex-col gap-2">
          <ErrorAlert error={change.error} />
          {accounts.length > 0 && (
            <ItemGroup>
              {accounts.map((account, index) => (
                <AccountRow
                  key={account.id}
                  account={account}
                  order={index + 1}
                  last={index === accounts.length - 1}
                  subscription={subscription}
                />
              ))}
            </ItemGroup>
          )}
          <ModelChips models={provider.models} />
        </div>
      </CardContent>
    </Card>
  )
}

// AccountRow is one account: where it stands in the order, who it is, the one thing wrong with it, and what the
// owner can do: take it out of use, and remove it when it was signed in here.
function AccountRow({
  account,
  order,
  last,
  subscription: owner,
}: {
  account: Account
  order: number
  // last: no account stands after it.
  last: boolean
  subscription: Subscription
}) {
  const refresh = useRefreshAccounts()
  const subscription = owner.name
  const state = accountState(account)

  // 9router moves a connection up and down the same way (providers/[id]/page.js).
  const move = useMutation({
    mutationFn: (direction: "up" | "down") =>
      moveAccount({ path: { account_id: account.id }, body: { direction } }),
    onSuccess: refresh,
  })
  const here = account.sign_in !== undefined
  const id = `use-${account.id}`

  const change = useMutation({
    mutationFn: (next: boolean) =>
      (next ? enableAccount : disableAccount)({ path: { account_id: account.id } }),
    onSuccess: async (_, next) => {
      await refresh()
      said(next ? `${account.label} is in use` : `${account.label} is out of use`)
    },
  })

  return (
    <div className={account.status === "disabled" ? "opacity-70" : undefined}>
      <Item variant="muted" size="sm" aria-label={`${subscription} account ${account.label}`}>
        <span className="flex items-center gap-0.5 self-start">
          <span
            className="pe-0.5 text-xs text-muted-foreground tabular-nums"
            title="The gateway tries the accounts in this order"
          >
            #{order}
          </span>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={`Move ${account.label} up`}
            title="Try this account earlier"
            disabled={order === 1 || move.isPending}
            onClick={() => move.mutate("up")}
          >
            <ArrowUp />
          </Button>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={`Move ${account.label} down`}
            title="Try this account later"
            disabled={last || move.isPending}
            onClick={() => move.mutate("down")}
          >
            <ArrowDown />
          </Button>
        </span>
        <ItemContent>
          <ItemTitle>
            {account.label}
            {account.email && (
              <span className="inline-flex items-center gap-1 font-normal text-muted-foreground">
                <Mail className="size-3" aria-hidden />
                {account.email}
              </span>
            )}
          </ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              {state && (
                <Badge variant={state.tone} title={state.why}>
                  <StateIcon tone={state.tone} />
                  {state.label}
                </Badge>
              )}
              {account.plan && <Fact icon={Crown}>{planName(account.plan)}</Fact>}
              <Fact icon={here ? KeyRound : MonitorSmartphone}>
                {here ? "Signed in here" : "Signed in on a machine"}
              </Fact>
            </span>
          </ItemDescription>
          <LimitBars account={account} />
          <ErrorAlert error={change.error ?? move.error} />
        </ItemContent>
        <ItemActions>
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <label htmlFor={id} className="inline-flex items-center gap-1">
              <Power className="size-3" aria-hidden />
              In use
            </label>
            <Switch
              id={id}
              size="sm"
              checked={account.status === "active"}
              disabled={change.isPending}
              onCheckedChange={(next) => change.mutate(next)}
            />
          </span>
          {here && (
            <AccountSignInDialog
              runtime={owner.runtime as SignInRuntime}
              name={subscription}
              account={account}
              trigger={
                <Button variant="ghost" size="sm" aria-label={`Sign ${account.label} in again`}>
                  <RefreshCw data-icon="inline-start" />
                  Sign in again
                </Button>
              }
            />
          )}
          {here && (
            <ConfirmDialog
              trigger={
                <Button variant="ghost" size="sm" aria-label={`Remove ${account.label}`}>
                  <Trash2 data-icon="inline-start" />
                  Remove
                </Button>
              }
              title={`Remove ${account.label}${account.email ? ` (${account.email})` : ""}?`}
              consequence={`Its sign-in is deleted from Hypomnema. Work that runs under it now ends with an error; later work uses the next ${subscription} account.`}
              action="Remove"
              run={() => removeAccount({ path: { account_id: account.id } })}
              onDone={refresh}
              done={`${account.label} was removed`}
            />
          )}
        </ItemActions>
      </Item>
    </div>
  )
}

// LimitBars are an account's limits: a meter a window with the part used and when it starts again, as the
// Machines page shows a machine's shares (machine-cells.tsx, after Beszel) and as 9router's quota cards
// (usage/components/ProviderLimits), T3 Code's limits (usage/UsageLimits.tsx) and Orca's status bar do for a
// subscription. Only an account signed in here can be asked. An account with nothing to show has no bars, and a
// line says so when the reading failed; the button asks the vendor now.
function LimitBars({ account }: { account: Account }) {
  const limits = useAccountLimits(account.id, account.sign_in === "active")
  const refresh = useRefreshLimits(account.id)
  const data = limits.data

  if (!data || data.unavailable === "not_given" || data.unavailable === "sign_in") return null

  const again = (
    <Button
      variant="ghost"
      size="icon-xs"
      aria-label={`Read the limits of ${account.label} again`}
      title="Read the limits again"
      disabled={refresh.isPending}
      onClick={() => refresh.mutate()}
    >
      <RefreshCw className={refresh.isPending ? "animate-spin" : undefined} />
    </Button>
  )

  if (data.windows.length === 0) {
    return (
      <p
        className="flex items-center gap-1.5 text-xs text-muted-foreground"
        title={data.detail}
        data-testid="limits-unavailable"
      >
        <TriangleAlert className="size-3 shrink-0" aria-hidden />
        {data.unavailable === "resting"
          ? "Its limits are read again in a few minutes."
          : "Its limits could not be read."}
        {again}
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-1 pt-0.5 text-xs" data-testid="limits">
      {data.windows.map((window) => (
        <div
          key={window.name}
          className="grid max-w-md grid-cols-[7.5rem_minmax(0,1fr)] items-center gap-x-2"
        >
          <span className="inline-flex items-center gap-1 text-muted-foreground">
            {window.name.startsWith("week") ? (
              <CalendarDays className="size-3 shrink-0" aria-hidden />
            ) : (
              <Timer className="size-3 shrink-0" aria-hidden />
            )}
            <span className="truncate">{windowName(window.name)}</span>
          </span>
          <Meter
            label={`${windowName(window.name)} used by ${account.label}`}
            value={window.used_percent}
            connected={account.status === "active"}
            title={`${Math.round(100 - window.used_percent)}% left`}
          />
          <span className="col-start-2 truncate text-muted-foreground">
            {window.resets_at ? `resets ${until(window.resets_at)}` : ""}
          </span>
        </div>
      ))}
      <p className="flex items-center gap-1 text-2xs text-muted-foreground">
        {data.read_at ? `Used, read ${since(data.read_at)}` : "Used"}
        {again}
      </p>
    </div>
  )
}

// windowName is a window as the page calls it: the server's name with a capital.
function windowName(name: string) {
  return name.charAt(0).toUpperCase() + name.slice(1)
}

// StateIcon says what kind of state a badge is: resting, off, or something the owner must look at.
function StateIcon({ tone }: { tone: Tone }) {
  if (tone === "warning") return <Timer aria-hidden />
  if (tone === "outline") return <Power aria-hidden />

  return <TriangleAlert aria-hidden />
}

// planName is a plan as its vendor writes it: the server keeps the vendor's lower-case word.
function planName(plan: string) {
  return plan.charAt(0).toUpperCase() + plan.slice(1)
}

function Fact({ icon: Icon, children }: { icon: LucideIcon; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <Icon className="size-3" aria-hidden />
      {children}
    </span>
  )
}

// ModelChips are the models a provider offers, each with its maker's mark and its name in full.
function ModelChips({ models }: { models: string[] }) {
  if (models.length === 0) return null

  return (
    <div className="flex flex-wrap items-center gap-1.5 px-1 pt-1 text-xs text-muted-foreground">
      <span className="inline-flex items-center gap-1 font-medium text-foreground">
        <Box className="size-3.5" aria-hidden />
        Models
      </span>
      {models.map((model) => (
        <span
          key={model}
          title={model}
          className="inline-flex items-center gap-1 rounded-md bg-foreground/5 px-1.5 leading-[18px]"
        >
          <ModelMark name={model} className="size-3 shrink-0" />
          {modelName(model)}
        </span>
      ))}
    </div>
  )
}

// ApiProviderCard is a provider reached with a key: its address, where its key is kept, never the key, and its
// models.
function ApiProviderCard({ provider }: { provider: Provider }) {
  return (
    <Card size="sm" data-testid="api-provider-card">
      <CardHeader>
        <CardTitle>
          <span className="flex items-center gap-3">
            <ProviderMark kind={provider.kind} className="size-6 shrink-0" />
            <span className="flex min-w-0 flex-col gap-0.5">
              <span>{provider.name}</span>
              <CardDescription>
                {provider.kind === "anthropic" ? "Anthropic API" : "OpenAI-compatible API"}
                {provider.base_url && ` · ${provider.base_url}`}
              </CardDescription>
            </span>
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="flex flex-col gap-2">
          {provider.api_key_ref && (
            <Item variant="muted" size="sm">
              <ItemContent>
                <ItemTitle>
                  <KeyRound className="size-3.5" aria-hidden />
                  API key
                  <span className="font-mono text-xs font-normal text-muted-foreground">
                    {provider.api_key_ref}
                  </span>
                </ItemTitle>
              </ItemContent>
            </Item>
          )}
          <ModelChips models={provider.models} />
        </div>
      </CardContent>
    </Card>
  )
}

// Routes are the gateway's models: on a wide screen a table, on a phone one block a model, since a row of
// targets does not fit beside a name there.
function Routes({ routes }: { routes: Route[] }) {
  return (
    <>
      <div className="hidden min-w-0 overflow-hidden rounded-xl border bg-card md:block">
        <Table aria-label="Models the gateway serves">
          <TableHeader>
            <TableRow>
              <TableHead>
                <span className="flex items-center gap-2">
                  <Tag className="size-4 text-muted-foreground" aria-hidden />
                  Name
                </span>
              </TableHead>
              <TableHead>
                <span className="flex items-center gap-2">
                  <RouteIcon className="size-4 text-muted-foreground" aria-hidden />
                  Served by, in order
                </span>
              </TableHead>
              <TableHead>
                <span className="flex items-center gap-2">
                  <Bot className="size-4 text-muted-foreground" aria-hidden />
                  Agents
                </span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {routes.map((route) => (
              <TableRow key={route.model.name}>
                <TableCell>
                  <RouteName route={route} />
                </TableCell>
                <TableCell>
                  <Targets route={route} />
                </TableCell>
                <TableCell>
                  <Offered route={route} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <div className="md:hidden">
        <ItemGroup>
          {routes.map((route) => (
            <Item key={route.model.name} variant="outline" size="sm">
              <ItemContent>
                <ItemTitle>
                  <RouteName route={route} />
                </ItemTitle>
                <Targets route={route} />
              </ItemContent>
              <ItemActions>
                <Offered route={route} />
              </ItemActions>
            </Item>
          ))}
        </ItemGroup>
      </div>
    </>
  )
}

function RouteName({ route }: { route: Route }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-2">
      <ModelMark name={route.model.name} className="size-4 shrink-0" />
      <span className="flex min-w-0 flex-col">
        <span className="font-medium">{modelName(route.model.name)}</span>
        <span className="font-mono text-2xs text-muted-foreground">{route.model.name}</span>
      </span>
    </span>
  )
}

function Targets({ route }: { route: Route }) {
  if (route.targets.length === 0) {
    return <span className="text-sm text-muted-foreground">Nowhere yet</span>
  }

  return (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm whitespace-normal">
      {route.targets.map((target, index) => (
        <span
          key={`${target.provider}:${target.upstream}`}
          className={
            index > 0
              ? "inline-flex items-center gap-1.5 text-muted-foreground"
              : "inline-flex items-center gap-1.5"
          }
        >
          {index > 0 && <span>then</span>}
          <ProviderMark kind={target.kind} className="size-3.5 shrink-0" />
          {target.provider}
          {target.upstream !== route.model.name && (
            <span className="font-mono text-2xs text-muted-foreground">{target.upstream}</span>
          )}
        </span>
      ))}
    </span>
  )
}

function Offered({ route }: { route: Route }) {
  return route.offered ? (
    <Badge variant="success" title="A profile or a Bot may name this model">
      <Link2 aria-hidden />
      Offered
    </Badge>
  ) : (
    <span className="text-xs text-muted-foreground">—</span>
  )
}

function count(n: number, noun: string) {
  return `${n} ${noun}${n === 1 ? "" : "s"}`
}
