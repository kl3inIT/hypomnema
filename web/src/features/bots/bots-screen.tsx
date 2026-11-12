import { Link } from "@tanstack/react-router"
import { Brain, History, NotebookText } from "lucide-react"
import { useState } from "react"

import type { Bot } from "@/api/generated/types.gen"
import { Button, buttonVariants } from "@/components/ui/button"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { effortLabel } from "@/features/agents/profiles-state"
import { PageHeader } from "@/features/shell/app-shell"
import { modelIsId } from "@/lib/model-name"
import { ModelMark } from "@/features/agents/model-mark"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { memoryLine, modelLine } from "./bots-state"
import { useBots } from "./use-bots"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.bots.layout"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// BotsScreen lists the Bots (issue #169), as cards or a table with the Machines page's switch. The list, its
// loading, error and empty states and the card are the Agents page's (features/agents/agents-screen.tsx), which
// took them from the Machines page (machines-screen.tsx, after Beszel's SystemCard).
export function BotsScreen() {
  const bots = useBots()
  const [layout, setLayout] = useState<Layout>(readLayout)
  const list = bots.data ?? []

  function choose(next: Layout) {
    setLayout(next)

    try {
      window.localStorage.setItem(layoutKey, next)
    } catch {
      // Kept until the page closes.
    }
  }

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title="Bots">
        <div className="ms-auto flex items-center gap-2">
          <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
            <legend className="sr-only">Layout</legend>
            {(["grid", "table"] as const).map((l) => (
              <button
                key={l}
                type="button"
                aria-pressed={layout === l}
                onClick={() => choose(l)}
                className="rounded-md px-2.5 py-1 text-xs text-muted-foreground capitalize aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
              >
                {l}
              </button>
            ))}
          </fieldset>
          <Link to="/bots/new" className={buttonVariants()}>
            New bot
          </Link>
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-8 sm:px-5">
        <p className="pb-3 text-xs text-muted-foreground">
          A Bot talks with you in chats, gives work to workers, and keeps a memory across its chats.
        </p>
        {bots.error ? (
          <div className="flex flex-col items-center gap-3 py-10 text-sm text-muted-foreground">
            <p>Couldn&apos;t load this list.</p>
            <Button variant="outline" size="sm" onClick={() => void bots.refetch()}>
              Retry
            </Button>
          </div>
        ) : bots.isPending ? (
          <div
            className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
            aria-label="Loading"
          >
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-48 animate-pulse rounded-xl bg-muted" />
            ))}
          </div>
        ) : list.length === 0 ? (
          <div className="mx-auto flex max-w-sm flex-col items-center gap-4 py-16 text-center">
            <p className="text-sm text-muted-foreground">
              No Bots yet. A Bot has a name, a model, a persona you write and a memory it keeps.
            </p>
            <Link to="/bots/new" className={buttonVariants()}>
              New bot
            </Link>
          </div>
        ) : layout === "table" ? (
          <BotsTable bots={list} />
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {list.map((bot) => (
              <BotCard key={bot.name} bot={bot} />
            ))}
          </div>
        )}
      </div>
    </main>
  )
}

// BotCard is the Agents page's card: a header with the face and the name, then the persona and one row a fact,
// each with its icon and its name. The name is a link that covers the card.
function BotCard({ bot }: { bot: Bot }) {
  const rows = [
    {
      name: "Model",
      // The model's maker, as the Agents cards show it (memory of a model: its logo and full name).
      icon: <ModelMark name={bot.model ?? ""} className="size-4" />,
      value: (
        <>
          <span className={cn("shrink-0 text-xs", modelIsId(bot.model) && "font-mono")}>
            {modelLine(bot)}
          </span>
          {bot.provider && (
            <span className="ms-1.5 truncate text-xs text-muted-foreground">({bot.provider})</span>
          )}
        </>
      ),
    },
    {
      name: "Effort",
      icon: <Brain className="size-4 text-muted-foreground" aria-hidden />,
      value: bot.effort ? effortLabel(bot.effort) : "Default",
    },
    {
      name: "Memory",
      icon: <NotebookText className="size-4 text-muted-foreground" aria-hidden />,
      value: memoryLine(bot),
    },
    {
      name: "Changed",
      icon: <History className="size-4 text-muted-foreground" aria-hidden />,
      value: since(bot.updated_at),
    },
  ]

  return (
    <article
      data-testid="bot"
      aria-label={bot.name}
      className="relative flex w-full flex-col overflow-hidden rounded-xl border bg-card text-card-foreground hover:border-foreground/25"
    >
      <header className="flex items-center gap-2 border-b border-border/60 bg-muted/30 px-4 py-2">
        <h2 className="flex min-w-0 flex-1 items-center gap-2.5 text-sm font-semibold">
          <AgentAvatar name={bot.name} className="size-8 shrink-0" />
          <Link
            to="/bots/$name"
            params={{ name: bot.name }}
            className="truncate outline-none after:absolute after:inset-0 after:rounded-xl focus-visible:after:ring-2 focus-visible:after:ring-ring"
          >
            {bot.name}
          </Link>
        </h2>
      </header>
      <p className="line-clamp-3 px-5 pt-3.5 text-sm text-foreground/85">
        {bot.persona || "No persona."}
      </p>
      <dl
        className="grid gap-2.5 px-5 pt-3.5 pb-4 text-sm"
        style={{ gridTemplateColumns: "24px minmax(64px, max-content) minmax(0, 1fr)" }}
      >
        {rows.map((row) => (
          <div key={row.name} className="col-span-3 grid grid-cols-subgrid items-center">
            <span className="flex items-center">{row.icon}</span>
            <dt className="pe-3 text-muted-foreground">{row.name}:</dt>
            <dd className="flex min-w-0 items-baseline">{row.value}</dd>
          </div>
        ))}
      </dl>
    </article>
  )
}

function BotsTable({ bots }: { bots: Bot[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            {["Name", "Model", "Effort", "Memory", "Changed"].map((name) => (
              <th
                key={name}
                scope="col"
                className="h-10 px-3 text-start text-xs font-medium whitespace-nowrap"
              >
                {name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {bots.map((bot) => (
            <tr key={bot.name} data-testid="bot" className="border-b last:border-b-0">
              <td className="max-w-80 px-3 py-2.5">
                <span className="flex min-w-0 items-center gap-2.5">
                  <AgentAvatar name={bot.name} className="size-8 shrink-0" />
                  <span className="min-w-0">
                    <Link
                      to="/bots/$name"
                      params={{ name: bot.name }}
                      className="font-medium hover:underline"
                    >
                      {bot.name}
                    </Link>
                    <span className="block truncate text-xs text-muted-foreground">
                      {bot.persona || "No persona."}
                    </span>
                  </span>
                </span>
              </td>
              <td className="px-3 py-2.5 whitespace-nowrap">
                <span className="inline-flex items-center gap-1.5">
                  <ModelMark name={bot.model ?? ""} className="size-3.5 shrink-0" />
                  <span className={cn("text-xs", modelIsId(bot.model) && "font-mono")}>
                    {modelLine(bot)}
                  </span>
                </span>
                {bot.provider && (
                  <span className="ms-1.5 text-xs text-muted-foreground">({bot.provider})</span>
                )}
              </td>
              <td className="px-3 py-2.5 whitespace-nowrap">
                {bot.effort ? effortLabel(bot.effort) : "Default"}
              </td>
              <td className="px-3 py-2.5 whitespace-nowrap">{memoryLine(bot)}</td>
              <td className="px-3 py-2.5 whitespace-nowrap text-muted-foreground">
                {since(bot.updated_at)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
