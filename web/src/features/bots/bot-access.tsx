import { useIsMutating } from "@tanstack/react-query"
import { Bot as BotIcon, Plug } from "lucide-react"
import type { ReactNode } from "react"

import type { AllowList, Bot, BotSettings } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Switch } from "@/components/ui/switch"
import { errorText } from "@/features/agents/profiles-state"
import { useProfiles } from "@/features/agents/use-profiles"
import { RuntimeMark } from "@/features/agents/runtime-mark"
import { IntegrationMark } from "@/features/settings/service-cards"
import { said, useServices } from "@/features/settings/use-integrations"

import { allowed, keptSettings, withName } from "./bots-state"
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

// BotAccess is the two cards on a Bot's page. The PUT carries the Bot's stored settings, so a switch changes only
// its list; what the form above holds unsaved stays in the form.
export function BotAccess({ bot }: { bot: Bot }) {
  const put = usePutBot()
  // Any save of this Bot, the form's too: a switch sends the stored model and persona, which the form may be
  // replacing at that moment.
  const saving = useIsMutating({ mutationKey: putBotKey }) > 0
  const services = useServices()
  const profiles = useProfiles()

  function save(change: Pick<BotSettings, "connections" | "workers">, what: string) {
    put.mutate(
      { name: bot.name, settings: { ...keptSettings(bot), ...change } },
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
    ...allowed(bot.connections, forChats).map<Item>((name) => ({
      name,
      label: <span className="shrink-0 font-medium">{name}</span>,
      detail: "Not used in chats",
    })),
  ]

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
      {put.error && (
        <Alert variant="destructive">
          <AlertDescription>{errorText(put.error)}</AlertDescription>
        </Alert>
      )}
    </div>
  )
}
