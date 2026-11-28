import { ChevronRight } from "lucide-react"

import type { Bot, BotVersion } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { savedAgo } from "@/features/agents/profiles-state"

import { versionLines, versionWho } from "./bots-state"
import { useBotVersions, useRestoreBotVersion } from "./use-bots"

// BotVersions lists a Bot's saved settings, newest first, each opening to what it set, with a way back to an
// earlier one (issue #1336). It is the Agents pages' versions list (features/agents/profile-screen.tsx), which is
// OpenMausBot's history of a bot's changes (Apache License 2.0, https://github.com/milind-soni/OpenMausBot;
// src/components/bot-settings/HistorySection.tsx): a row a save with who made it and when, and a restore that adds
// a new version and removes none, as Paperclip's rollback of an agent's config revision
// (server/src/services/agents.ts, rollbackConfigRevision).
export function BotVersions({ bot, onRestored }: { bot: Bot; onRestored: () => void }) {
  const versions = useBotVersions(bot.id)
  const restore = useRestoreBotVersion()

  return (
    <section aria-labelledby="bot-versions" className="flex flex-col gap-2 border-t pt-4">
      <div className="flex flex-wrap items-baseline gap-x-3">
        <h3 id="bot-versions" className="text-sm font-medium">
          Versions
        </h3>
        <p className="text-xs text-muted-foreground">
          Each save of the settings above keeps one. The name and the memory are not part of them.
        </p>
      </div>
      {versions.error ? (
        <p className="py-2 text-sm text-destructive">The versions could not be read.</p>
      ) : !versions.data ? (
        <p className="py-2 text-sm text-muted-foreground">Loading…</p>
      ) : versions.data.length === 0 ? (
        <p className="py-2 text-sm text-muted-foreground">
          None yet: the next save keeps what the Bot has now as its first version.
        </p>
      ) : (
        <ol className="flex flex-col gap-2" aria-label="Versions">
          {versions.data.map((v, index) => (
            <Version
              key={v.id}
              bot={bot}
              version={v}
              current={index === 0}
              restore={() => restore.mutateAsync({ id: bot.id, versionId: v.id })}
              restored={async () => {
                onRestored()
                await versions.refetch()
              }}
            />
          ))}
        </ol>
      )}
    </section>
  )
}

function Version({
  bot,
  version: v,
  current,
  restore,
  restored,
}: {
  bot: Bot
  version: BotVersion
  current: boolean
  restore: () => Promise<unknown>
  restored: () => Promise<unknown>
}) {
  return (
    <li className="rounded-lg border bg-card">
      <details className="group">
        <summary className="flex cursor-pointer list-none flex-wrap items-center gap-x-2 gap-y-0.5 px-3 py-2.5 text-sm">
          <ChevronRight
            className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90"
            aria-hidden
          />
          <span className="font-medium">Version {v.version}</span>
          <span className="text-xs text-muted-foreground">
            · {versionWho(v)}
            {savedAgo(v) ? ` · ${savedAgo(v)}` : ""}
          </span>
          {current && <span className="ms-auto text-xs text-muted-foreground">Current</span>}
        </summary>
        <div className="flex flex-col gap-3 border-t px-3 py-3">
          <dl className="grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-xs">
            {versionLines(v.settings).map(([term, value]) => (
              <div key={term} className="contents">
                <dt className="text-muted-foreground">{term}</dt>
                <dd className="min-w-0 break-words">{value}</dd>
              </div>
            ))}
          </dl>
          {v.settings.instructions && (
            <pre className="overflow-x-auto rounded-md bg-muted px-3 py-2.5 font-mono text-xs leading-relaxed whitespace-pre-wrap">
              {v.settings.instructions}
            </pre>
          )}
          {!current && (
            <div>
              <ConfirmDialog
                trigger={
                  <Button variant="outline" size="sm">
                    Restore this version
                  </Button>
                }
                title={`Restore version ${v.version}?`}
                consequence={`Saves these settings for ${bot.name} as a new version. Its name and memory stay, and the versions after this one stay in the list.`}
                action="Restore"
                run={restore}
                onDone={restored}
                done={`Version ${v.version} restored`}
              />
            </div>
          )}
        </div>
      </details>
    </li>
  )
}
