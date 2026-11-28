import { Trash2 } from "lucide-react"
import { useState } from "react"

import type { Bot } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { errorText } from "@/lib/server-error"

import { useSetBotRepos } from "./use-bots"

// BotRepos are the repositories whose work the Bot owns (issue #1334): a Run created on one goes to the Bot's chat,
// as a Paperclip project names its lead agent. Each change is saved at once with the whole list, as the Channels and
// Memory parts of this page save apart from the settings form. The server refuses a repository another Bot owns, and
// says which; the alert keeps its words. The main Bot owns none: it takes the Runs no other Bot owns.
export function BotRepos({ bot }: { bot: Bot }) {
  const save = useSetBotRepos()
  const [repo, setRepo] = useState("")
  const repos = bot.repos ?? []
  const typed = repo.trim()

  const saveRepos = (next: string[], onDone?: () => void) => {
    save.mutate({ id: bot.id, repos: next }, { onSuccess: onDone })
  }

  if (bot.main) {
    return (
      <section aria-labelledby="bot-repos" className="flex flex-col gap-1 border-t pt-4">
        <h3 id="bot-repos" className="text-sm font-medium">
          Repositories
        </h3>
        <p className="text-xs text-muted-foreground">
          The main Bot owns no repository: it takes the Runs of every repository no other Bot owns.
        </p>
      </section>
    )
  }

  return (
    <section aria-labelledby="bot-repos" className="flex flex-col gap-2 border-t pt-4">
      <h3 id="bot-repos" className="text-sm font-medium">
        Repositories
      </h3>
      {repos.length > 0 && (
        <ul aria-label="Repositories it owns" className="flex flex-col gap-1">
          {repos.map((name) => (
            <li key={name} className="flex min-w-0 items-center gap-2 text-sm">
              <span className="min-w-0 flex-1 truncate font-mono text-xs">{name}</span>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`Stop owning ${name}`}
                disabled={save.isPending}
                onClick={() => saveRepos(repos.filter((r) => r !== name))}
              >
                <Trash2 aria-hidden />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <form
        aria-label="Add a repository"
        onSubmit={(event) => {
          event.preventDefault()
          if (typed) saveRepos([...repos, typed], () => setRepo(""))
        }}
      >
        <Field>
          <div className="flex gap-2">
            <Input
              id="bot-repo-add"
              aria-label="Repository"
              autoComplete="off"
              spellCheck={false}
              placeholder="owner/name"
              value={repo}
              onChange={(event) => {
                save.reset()
                setRepo(event.target.value)
              }}
            />
            <Button
              type="submit"
              variant="outline"
              focusableWhenDisabled={save.isPending}
              disabled={!typed || save.isPending}
            >
              {save.isPending && <Spinner data-icon="inline-start" />}
              Add
            </Button>
          </div>
          <FieldDescription>
            {repos.length === 0
              ? "It owns no repository: Runs go to the chat that creates them, or to the main Bot. "
              : ""}
            A Run created on one of these goes to this Bot&apos;s main chat, from the CLI or from
            another Bot. Name it as owner/name, its URL, or a registered name. A repository has one
            owning Bot.
          </FieldDescription>
        </Field>
      </form>
      {save.error && (
        <Alert variant="destructive">
          <AlertDescription>{errorText(save.error)}</AlertDescription>
        </Alert>
      )}
    </section>
  )
}
