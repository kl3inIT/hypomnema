import { useIsMutating, useQuery } from "@tanstack/react-query"
import { Bot as BotIcon, Gauge, LayoutDashboard, Plug, Wrench } from "lucide-react"
import { useState, type ReactNode } from "react"

import "@/api/client"
import { listSupersetDashboardsOptions } from "@/api/generated/@tanstack/react-query.gen"
import type { AllowList, Bot, BotSettings, ToolDisplay } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldLabel,
  FieldTitle,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Switch } from "@/components/ui/switch"
import { errorText } from "@/lib/server-error"
import { useProfiles } from "@/features/agents/use-profiles"
import { RuntimeMark } from "@/components/icons/runtime-mark"
import { IntegrationMark } from "@/features/settings/service-cards"
import { said } from "@/lib/said"
import { useHostKinds, useServices } from "@/features/settings/use-integrations"

import { allowed, keptSettings, scopeList, withName, withScope } from "./bots-state"
import { putBotKey, usePutBot } from "./use-bots"

// What a Bot may use (issue #169): the Connections it may use and the worker profiles it gives work to. Each is a
// card with a switch that limits it, then a switch per item, as OpenMausBot's bot settings limit the connected apps
// (src/components/bot-settings/AccessSection.tsx L731-784, ConnectorScopesControl L894-928: off, every app; on,
// only those switched on, starting with none). A change is saved at once, as there. Changed: a switch per item
// instead of off, read and read-write; the workers' card is the same shape (no reference has one on a page).

type Item = { name: string; label: ReactNode; detail?: string }

function AccessCard({
  id,
  title,
  icon,
  limit,
  all,
  none,
  list,
  items,
  empty,
  pending,
  onChange,
}: {
  id: string
  title: string
  icon: ReactNode
  // limit names the switch; all and none say what the Bot may use without a list and with an empty one.
  limit: string
  all: string
  none: string
  list: AllowList
  items: Item[]
  empty: string
  pending: boolean
  onChange: (list: AllowList) => void
}) {
  const on = list.names.length
  const line = !list.limited ? all : on === 0 ? none : `${on} of ${items.length} on`

  return (
    <section aria-labelledby={id} className="flex flex-col gap-3 rounded-xl border bg-card p-4">
      <div className="flex items-start gap-3">
        <span className="mt-0.5 shrink-0 text-primary">{icon}</span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h3 id={id} className="text-sm font-medium">
            {title}
          </h3>
          <p className="text-xs text-muted-foreground">{line}</p>
        </div>
        <span className="flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
          <label htmlFor={`${id}-limit`}>{limit}</label>
          <Switch
            id={`${id}-limit`}
            checked={list.limited}
            disabled={pending}
            onCheckedChange={(limited) => onChange({ limited, names: [] })}
          />
        </span>
      </div>
      {list.limited && (
        <ul className="flex flex-col divide-y rounded-lg border">
          {items.length === 0 && (
            <li className="px-3 py-2.5 text-xs text-muted-foreground">{empty}</li>
          )}
          {items.map((item) => (
            <li key={item.name} className="flex min-w-0 items-center gap-3 px-3 py-2">
              <span className="flex min-w-0 flex-1 items-center gap-2 text-[13px]">
                {item.label}
                {item.detail && (
                  <span
                    className="min-w-0 truncate text-xs text-muted-foreground"
                    title={item.detail}
                  >
                    {item.detail}
                  </span>
                )}
              </span>
              <Switch
                size="sm"
                aria-label={`${title}: ${item.name}`}
                checked={list.names.includes(item.name)}
                disabled={pending}
                onCheckedChange={(next) =>
                  onChange({ limited: true, names: withName(list.names, item.name, next) })
                }
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

// toolDisplays are the levels of what a Bot's chats show of its tool calls (issue #441), OpenClaw's verbose levels
// per agent (off, on, full), each with what people see, the owner included, on the web and at Lark.
const toolDisplays: { value: ToolDisplay; title: string; line: string }[] = [
  { value: "off", title: "Off", line: "Only the answers: no tool call is shown." },
  {
    value: "on",
    title: "On",
    line: "A row per tool call with its tool's name, without its command or output.",
  },
  {
    value: "full",
    title: "Full",
    line: "Each row opens to the command or arguments, the output and the time.",
  },
]

function ToolDisplayCard({
  bot,
  pending,
  onChange,
}: {
  bot: Bot
  pending: boolean
  onChange: (display: ToolDisplay) => void
}) {
  return (
    <section
      aria-labelledby="bot-tool-display"
      className="flex flex-col gap-3 rounded-xl border bg-card p-4"
    >
      <div className="flex items-start gap-3">
        <span className="mt-0.5 shrink-0 text-primary">
          <Wrench className="size-4" aria-hidden />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h3 id="bot-tool-display" className="text-sm font-medium">
            Tool calls in chats
          </h3>
          <p className="text-xs text-muted-foreground">
            What everyone who reads its chats sees, you included, here and at Lark.
          </p>
        </div>
      </div>
      <RadioGroup
        aria-labelledby="bot-tool-display"
        value={bot.tool_display}
        disabled={pending}
        onValueChange={(next) => onChange(next as ToolDisplay)}
      >
        {toolDisplays.map((d) => (
          <FieldLabel key={d.value} htmlFor={`tool-display-${d.value}`}>
            <Field orientation="horizontal">
              <RadioGroupItem id={`tool-display-${d.value}`} value={d.value} />
              <FieldContent>
                <FieldTitle>{d.title}</FieldTitle>
                <FieldDescription>{d.line}</FieldDescription>
              </FieldContent>
            </Field>
          </FieldLabel>
        ))}
      </RadioGroup>
    </section>
  )
}

// GuaranteedWorkersCard is a Bot's guaranteed share of the machines' room, in workers (issue #731; its limit,
// "Workers at once", until 2026-11-17), as Kueue's nominalQuota and YARN's capacity: a first claim on room as it
// frees, not a cap; the Bot may use more while the others leave room free, and nothing running is stopped. It is
// saved with its button, a number being typed one digit at a time.
function GuaranteedWorkersCard({
  bot,
  pending,
  onSave,
}: {
  bot: Bot
  pending: boolean
  onSave: (max: number) => void
}) {
  const [max, setMax] = useState(String(bot.guaranteed_dispatches))
  const n = Number(max)
  const unchanged = !Number.isInteger(n) || n < 1 || n > 64 || n === bot.guaranteed_dispatches

  return (
    <section
      aria-labelledby="bot-guaranteed-workers"
      className="flex flex-col gap-3 rounded-xl border bg-card p-4"
    >
      <div className="flex items-start gap-3">
        <span className="mt-0.5 shrink-0 text-primary">
          <Gauge className="size-4" aria-hidden />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h3 id="bot-guaranteed-workers" className="text-sm font-medium">
            Guaranteed workers
          </h3>
          <p className="text-xs text-muted-foreground">
            Its share of the machines&apos; room: when room frees, the Bot furthest below its share
            goes first. It may run more while other Bots leave room free; nothing running is
            stopped.
          </p>
        </div>
      </div>
      <form
        className="flex items-center gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          if (!unchanged) onSave(n)
        }}
      >
        <div className="w-24">
          <Input
            aria-labelledby="bot-guaranteed-workers"
            type="number"
            required
            min={1}
            max={64}
            step={1}
            inputMode="numeric"
            value={max}
            onChange={(event) => setMax(event.target.value)}
          />
        </div>
        <Button
          type="submit"
          variant="outline"
          aria-label="Save guaranteed workers"
          disabled={pending || unchanged}
        >
          Save
        </Button>
      </form>
    </section>
  )
}

// DashboardsCard is the dashboards a Bot may read of one Superset Connection it may use (issue #562), the same card
// as the Connections', under them: OpenMausBot's app shows the scope control beside the connected app
// (connector-scopes.ts L4). Off, the Bot reads every dashboard the account sees and may send its own requests to
// Superset's API; on, only the dashboards switched on, through the dashboard and chart reads. The dashboards are
// the account's, read as the owner; one the scope names that the account no longer sees is kept on it.
function DashboardsCard({
  bot,
  label,
  pending,
  onChange,
}: {
  bot: Bot
  label: string
  pending: boolean
  onChange: (list: AllowList) => void
}) {
  const dashboards = useQuery({
    ...listSupersetDashboardsOptions({ path: { label } }),
    select: (page) => page.data,
  })
  const list = scopeList(bot.scopes, `superset:${label}`)
  const seen = (dashboards.data ?? []).map<Item>((d) => ({
    name: String(d.id),
    // A long title is cut, its number never is: it is what the Bot's scope keeps.
    label: (
      <>
        <span className="min-w-0 truncate font-medium" title={d.title}>
          {d.title}
        </span>
        <span className="shrink-0 text-xs text-muted-foreground">#{d.id}</span>
      </>
    ),
  }))
  const items = [
    ...seen,
    ...allowed(list, seen).map<Item>((name) => ({
      name,
      label: <span className="shrink-0 font-medium">#{name}</span>,
      detail: "Not seen by the account",
    })),
  ]
  const empty = dashboards.isPending
    ? "Reading the dashboards…"
    : dashboards.error
      ? `The dashboards could not be read: ${errorText(dashboards.error)}`
      : "The account sees no dashboard."

  return (
    <AccessCard
      id={`bot-dashboards-${label}`}
      title={`Superset · ${label} dashboards`}
      icon={<LayoutDashboard className="size-4" aria-hidden />}
      limit="Limit dashboards"
      all="Every dashboard the account sees, and its own requests to Superset's API."
      none="No dashboard: it reads nothing of this account."
      list={list}
      items={items}
      empty={empty}
      pending={pending}
      onChange={onChange}
    />
  )
}

// BotAccess is the cards on a Bot's page. The PUT carries the Bot's stored settings, so a switch changes only
// its list; what the form above holds unsaved stays in the form.
export function BotAccess({ bot }: { bot: Bot }) {
  const put = usePutBot()
  // Any save of this Bot, the form's too: a switch sends the stored model and persona, which the form may be
  // replacing at that moment.
  const saving = useIsMutating({ mutationKey: putBotKey }) > 0
  const services = useServices()
  const kinds = useHostKinds()
  const profiles = useProfiles()

  function save(
    change: Pick<
      BotSettings,
      "connections" | "workers" | "scopes" | "tool_display" | "guaranteed_dispatches"
    >,
    what: string,
  ) {
    put.mutate(
      { id: bot.id, settings: { ...keptSettings(bot), ...change } },
      { onSuccess: () => said(what) },
    )
  }

  // The Connections are those the owner switched on for chats, and any the list still names: a Connection off for
  // chats is off for every Bot.
  const forChats = (services.data ?? []).flatMap((service) =>
    service.connections
      .filter((c) => c.chats)
      .map<Item>((c) => ({
        name: `${service.id}:${c.label}`,
        label: (
          <>
            <IntegrationMark id={service.id} className="size-4 shrink-0" />
            <span className="shrink-0 font-medium">
              {service.name} · {c.label}
            </span>
          </>
        ),
        detail: c.account,
      })),
  )
  const connections = [
    ...forChats,
    ...allowed(bot.connections, forChats).map<Item>((name) => {
      const [integration = ""] = name.split(":")

      return {
        name,
        label: (
          <>
            <IntegrationMark
              id={integration}
              kind={kinds[integration]}
              className="size-4 shrink-0"
            />
            <span className="shrink-0 font-medium">{name}</span>
          </>
        ),
        detail: "Not used in chats",
      }
    }),
  ]

  // The Superset Connections the Bot may use, each with the dashboards it may read.
  const supersets = forChats
    .filter(
      (item) =>
        item.name.startsWith("superset:") &&
        (!bot.connections.limited || bot.connections.names.includes(item.name)),
    )
    .map((item) => item.name.slice("superset:".length))

  const workerItems = (profiles.data ?? []).map<Item>((p) => ({
    name: p.name,
    label: (
      <>
        <RuntimeMark runtime={p.runtime} className="size-4 shrink-0" />
        <span className="shrink-0 font-medium">{p.name}</span>
      </>
    ),
    detail: p.description,
  }))
  const workers = [
    ...workerItems,
    ...allowed(bot.workers, workerItems).map<Item>((name) => ({
      name,
      label: <span className="shrink-0 font-medium">{name}</span>,
      detail: "No longer a profile",
    })),
  ]

  return (
    <div className="flex flex-col gap-3 border-t pt-4">
      <AccessCard
        id="bot-connections"
        title="Connections"
        icon={<Plug className="size-4" aria-hidden />}
        limit="Limit Connections"
        all="Every account switched on for chats in Integrations."
        none="No account: every Connection is off for this Bot."
        list={bot.connections}
        items={connections}
        empty="No account is switched on for chats. Switch one on in Settings, Integrations."
        pending={saving}
        onChange={(connections) =>
          save(
            { connections },
            connections.limited
              ? `Connections of ${bot.name} saved`
              : `${bot.name} may use every Connection`,
          )
        }
      />
      {supersets.map((label) => (
        <DashboardsCard
          key={label}
          bot={bot}
          label={label}
          pending={saving}
          onChange={(list) =>
            save(
              { scopes: withScope(bot.scopes, `superset:${label}`, list) },
              list.limited
                ? `Dashboards of ${bot.name} saved`
                : `${bot.name} may read every dashboard of superset:${label}`,
            )
          }
        />
      ))}
      <AccessCard
        id="bot-workers"
        title="Workers"
        icon={<BotIcon className="size-4" aria-hidden />}
        limit="Limit workers"
        all="Every worker profile. A Task names the one it is for."
        none="No worker: a Task this Bot creates is refused."
        list={bot.workers}
        items={workers}
        empty="There are no worker profiles. Make one on the Agents page."
        pending={saving}
        onChange={(workers) =>
          save(
            { workers },
            workers.limited ? `Workers of ${bot.name} saved` : `${bot.name} may use every worker`,
          )
        }
      />
      <GuaranteedWorkersCard
        key={bot.guaranteed_dispatches}
        bot={bot}
        pending={saving}
        onSave={(guaranteed_dispatches) =>
          save(
            { guaranteed_dispatches },
            `${bot.name} is guaranteed ${guaranteed_dispatches} workers`,
          )
        }
      />
      <ToolDisplayCard
        bot={bot}
        pending={saving}
        onChange={(tool_display) =>
          save({ tool_display }, `${bot.name} shows tool calls: ${tool_display}`)
        }
      />
      {put.error && (
        <Alert variant="destructive">
          <AlertDescription>{errorText(put.error)}</AlertDescription>
        </Alert>
      )}
    </div>
  )
}
