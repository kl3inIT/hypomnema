import { Dialog } from "@base-ui/react/dialog"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { FolderGit2 } from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { deleteRepo, putRepo } from "@/api/generated/sdk.gen"
import type { Repo } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"

import { SettingsPage } from "./settings-shell"
import { errorText, nameOf, reposKey, useRepos } from "./use-integrations"

const inputClass = "h-9 rounded-md border border-input bg-background px-3 text-sm"
const popupClass =
  "fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100svh-2rem)] w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto rounded-xl border bg-background p-5 outline-none"
const backdropClass =
  "fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0"

// RepositoriesScreen lists the repositories registered under a name, which a Run can then be started on by that
// name. A list of rows with one action each, as t3code's and Orca's settings list their projects and repositories.
export function RepositoriesScreen() {
  const repos = useRepos()

  return (
    <SettingsPage
      title="Repositories"
      description="Repositories registered under a short name, so that work can be given by that name."
      actions={<AddRepo />}
    >
      {repos.isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {repos.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(repos.error)}
        </p>
      )}
      {repos.data?.length === 0 && (
        <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed px-4 py-10 text-center">
          <FolderGit2 className="size-5 text-muted-foreground" aria-hidden />
          <p className="text-sm font-medium">No repositories yet</p>
          <p className="max-w-sm text-xs text-muted-foreground">
            A Run can always name a repository by its URL. Register the ones you use often to call
            them by a short name.
          </p>
        </div>
      )}
      {repos.data && repos.data.length > 0 && (
        <ul className="flex flex-col divide-y rounded-xl border bg-card">
          {repos.data.map((repo) => (
            <RepoRow key={repo.name} repo={repo} />
          ))}
        </ul>
      )}
    </SettingsPage>
  )
}

function RepoRow({ repo }: { repo: Repo }) {
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
      <FolderGit2 className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <div className="min-w-0 flex-1 basis-56 space-y-0.5">
        <p className="truncate text-sm font-medium">{repo.name}</p>
        <p className="truncate text-xs text-muted-foreground">
          {repo.host}/{repo.path}
          {repo.base ? ` · starts from ${repo.base}` : ""}
        </p>
      </div>
      <RemoveRepo repo={repo} />
    </li>
  )
}

function useRefreshRepos() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: reposKey })
}

function RemoveRepo({ repo }: { repo: Repo }) {
  const refresh = useRefreshRepos()
  const [open, setOpen] = useState(false)
  const remove = useMutation({
    mutationFn: () => deleteRepo({ path: { name: repo.name } }),
    onSuccess: async () => {
      await refresh()
      setOpen(false)
    },
  })

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) remove.reset()
      }}
    >
      <Dialog.Trigger
        render={<Button variant="ghost" size="sm" aria-label={`Remove ${repo.name}`} />}
      >
        Remove
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className={backdropClass} />
        <Dialog.Popup className={popupClass}>
          <Dialog.Title className="text-sm font-medium">Remove {repo.name}?</Dialog.Title>
          <Dialog.Description className="text-sm text-muted-foreground">
            Only the name is forgotten. The repository itself and the Runs on it are not touched.
          </Dialog.Description>
          {remove.error && (
            <p role="alert" className="text-sm text-destructive">
              {errorText(remove.error)}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              Remove repository
            </Button>
          </div>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

// AddRepo registers a repository: where it is, the name to call it by, and the branch work starts from.
function AddRepo() {
  const refresh = useRefreshRepos()
  const [open, setOpen] = useState(false)
  const [repo, setRepo] = useState("")
  const [name, setName] = useState<string | null>(null)
  const [base, setBase] = useState("")
  const called = name ?? nameOf(repo)

  const add = useMutation({
    mutationFn: () =>
      putRepo({
        path: { name: called.trim() },
        body: { repo: repo.trim(), ...(base.trim() ? { base: base.trim() } : {}) },
      }),
    onSuccess: async () => {
      await refresh()
      change(false)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setRepo("")
      setName(null)
      setBase("")
      add.reset()
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={change}>
      <Dialog.Trigger render={<Button />}>Add repository</Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className={backdropClass} />
        <Dialog.Popup className={popupClass}>
          <form
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              add.mutate()
            }}
          >
            <Dialog.Title className="text-sm font-medium">Add a repository</Dialog.Title>
            <div className="flex flex-col gap-1.5 text-sm">
              <label htmlFor="repo-address">Repository</label>
              <input
                id="repo-address"
                required
                autoComplete="off"
                aria-describedby="repo-address-hint"
                placeholder="https://gitlab.company.example/team/app"
                value={repo}
                onChange={(event) => setRepo(event.target.value)}
                className={inputClass}
              />
              <span id="repo-address-hint" className="text-xs text-muted-foreground">
                Its URL, or owner/name for one on GitHub. A company&apos;s server is added under
                Integrations first.
              </span>
            </div>
            <label className="flex flex-col gap-1.5 text-sm">
              Name
              <input
                required
                autoComplete="off"
                value={called}
                onChange={(event) => setName(event.target.value)}
                className={inputClass}
              />
            </label>
            <label className="flex flex-col gap-1.5 text-sm">
              Base branch
              <input
                autoComplete="off"
                placeholder="The repository's default"
                value={base}
                onChange={(event) => setBase(event.target.value)}
                className={inputClass}
              />
            </label>
            {add.error && (
              <p role="alert" className="text-sm text-destructive">
                {errorText(add.error)}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
              <Button
                type="submit"
                disabled={add.isPending || repo.trim() === "" || called.trim() === ""}
              >
                {add.isPending ? "Adding…" : "Add"}
              </Button>
            </div>
          </form>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
