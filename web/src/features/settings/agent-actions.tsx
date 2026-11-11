import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { cn } from "cn"
import { ChevronRight, ShieldCheck } from "lucide-react"

import "@/api/client"
import { listConnectionActions, setConnectionAction } from "@/api/generated/sdk.gen"
import type {
  ActionPolicy,
  ConnectionAction,
  Integration,
  IntegrationConnection,
} from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Item,
  ItemContent,
  ItemDescription,
  ItemFooter,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

import { ErrorAlert } from "./settings-parts"
import { said } from "./use-integrations"

// What agents may do with an account, action by action (issue #154). The list and its three answers follow
// Onyx's Tools tab of an app (Auto-approve, Ask, Disabled for each action); the grouping by service and the
// one-line tally follow affectUI's permissions screen.

const words: Record<ActionPolicy, string> = { allow: "On its own", ask: "Ask", never: "Never" }
const order: ActionPolicy[] = ["allow", "ask", "never"]
// The mark of the answer an action has now: the app's own tones for running, waiting for the owner, and off.
const dots: Record<ActionPolicy, string> = {
  allow: "bg-info-foreground",
  ask: "bg-needs-you",
  never: "bg-muted-foreground",
}

function actionsKey(integration: string, label: string) {
  return ["connection-actions", integration, label] as const
}

// tally says a list of actions in one line: "5 on their own · ask before 6 · never 2".
function tally(actions: ConnectionAction[]): string {
  const count = (policy: ActionPolicy) => actions.filter((a) => a.policy === policy).length
  const parts = [
    count("allow") && `${count("allow")} on their own`,
    count("ask") && `ask before ${count("ask")}`,
    count("never") && `never ${count("never")}`,
  ]

  return parts.filter(Boolean).join(" · ")
}

// AgentActions is the line under an account that says what agents may do with it, and the sheet that changes it.
// An account whose service does not go through the service proxy has no actions and shows nothing.
export function AgentActions({
  service,
  connection,
}: {
  service: Integration
  connection: IntegrationConnection
}) {
  const path = { integration: service.id, label: connection.label }
  const actions = useQuery({
    queryKey: actionsKey(service.id, connection.label),
    queryFn: async () => (await listConnectionActions({ path })).data.data,
  })

  if (!actions.data?.length) return null

  const services = [...new Set(actions.data.map((a) => a.service))]

  return (
    <Sheet>
      {/* The line starts where the account's name does. */}
      <div className="flex px-1">
        <SheetTrigger
          render={
            <Button
              variant="ghost"
              size="sm"
              aria-label={`What agents may do with ${connection.label}`}
            />
          }
        >
          <ShieldCheck data-icon="inline-start" />
          <span className="font-medium">Agents may</span>
          <span className="min-w-0 truncate font-normal text-muted-foreground">
            {tally(actions.data)}
          </span>
          <ChevronRight data-icon="inline-end" />
        </SheetTrigger>
      </div>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>
            {service.name} · {connection.label}
          </SheetTitle>
          <SheetDescription>
            What pi and workers may do with this account. On its own runs at once. Ask waits for
            your approval, in the chat or in the Task. Never is refused.
          </SheetDescription>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 pb-6">
          {services.map((name) => (
            <section key={name} aria-label={name} className="flex flex-col gap-2">
              <h3 className="text-xs font-medium text-muted-foreground">{name}</h3>
              <ItemGroup>
                {actions.data
                  .filter((a) => a.service === name)
                  .map((action) => (
                    <ActionRow
                      key={action.id}
                      integration={service.id}
                      label={connection.label}
                      action={action}
                    />
                  ))}
              </ItemGroup>
            </section>
          ))}
          <p className="text-xs text-muted-foreground">
            A request to {service.name} that is none of these always asks. Every request is recorded
            with who made it.
          </p>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function ActionRow({
  integration,
  label,
  action,
}: {
  integration: string
  label: string
  action: ConnectionAction
}) {
  const queryClient = useQueryClient()
  const change = useMutation({
    mutationFn: (policy: ActionPolicy) =>
      setConnectionAction({ path: { integration, label, action: action.id }, body: { policy } }),
    onSuccess: async (_, policy) => {
      await queryClient.invalidateQueries({ queryKey: actionsKey(integration, label) })
      said(`${action.name}: ${words[policy].toLowerCase()}`)
    },
  })

  return (
    <Item variant="outline" size="sm">
      <ItemContent>
        <ItemTitle>{action.name}</ItemTitle>
        {action.text && <ItemDescription>{action.text}</ItemDescription>}
        {change.error && <ErrorAlert error={change.error} />}
      </ItemContent>
      <ItemFooter>
        <ToggleGroup
          variant="outline"
          size="sm"
          spacing={0}
          aria-label={action.name}
          value={[action.policy]}
          disabled={change.isPending}
          onValueChange={(next) => {
            const policy = next[0] as ActionPolicy | undefined
            if (policy && policy !== action.policy) change.mutate(policy)
          }}
        >
          {order.map((policy) => (
            <ToggleGroupItem key={policy} value={policy}>
              {/* The answer in force is said twice, by a mark and by its weight: the group's own pressed
                  tone is too quiet to read in the dark theme. */}
              {policy === action.policy ? (
                <>
                  <span className={cn("size-1.5 rounded-full", dots[policy])} aria-hidden />
                  {words[policy]}
                </>
              ) : (
                <span className="font-normal text-muted-foreground">{words[policy]}</span>
              )}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </ItemFooter>
    </Item>
  )
}
