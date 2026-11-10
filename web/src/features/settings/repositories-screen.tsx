import { useForm } from "@tanstack/react-form"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { FolderGit2, Plus } from "lucide-react"
import { useState } from "react"
import { z } from "zod"

import "@/api/client"
import { deleteRepo, putRepo } from "@/api/generated/sdk.gen"
import type { Repo } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { FieldGroup } from "@/components/ui/field"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"

import { ConfirmDialog, ErrorAlert, TextField } from "./settings-parts"
import { SettingsPage } from "./settings-shell"
import { nameOf, reposKey, useRepos } from "./use-integrations"

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
      {repos.isPending && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      )}
      <ErrorAlert error={repos.error} />
      {repos.data?.length === 0 && (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <FolderGit2 />
            </EmptyMedia>
            <EmptyTitle>No repositories yet</EmptyTitle>
            <EmptyDescription>
              A Run can always name a repository by its URL. Register the ones you use often to call
              them by a short name.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {repos.data && repos.data.length > 0 && (
        <ItemGroup>
          {repos.data.map((repo) => (
            <RepoRow key={repo.name} repo={repo} />
          ))}
        </ItemGroup>
      )}
    </SettingsPage>
  )
}

function useRefreshRepos() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: reposKey })
}

function RepoRow({ repo }: { repo: Repo }) {
  const refresh = useRefreshRepos()

  return (
    <Item variant="outline">
      <ItemMedia variant="icon">
        <FolderGit2 />
      </ItemMedia>
      <ItemContent>
        <ItemTitle>{repo.name}</ItemTitle>
        <ItemDescription>
          {repo.host}/{repo.path}
          {repo.base ? ` · starts from ${repo.base}` : ""}
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        <ConfirmDialog
          trigger={
            <Button variant="ghost" size="sm" aria-label={`Remove ${repo.name}`}>
              Remove
            </Button>
          }
          title={`Remove ${repo.name}?`}
          consequence="Only the name is forgotten. The repository itself and the Runs on it are not touched."
          action="Remove repository"
          run={() => deleteRepo({ path: { name: repo.name } })}
          onDone={refresh}
        />
      </ItemActions>
    </Item>
  )
}

const repoSchema = z.object({
  repo: z.string().trim().min(1, "Give the repository's URL, or owner/name for one on GitHub."),
  name: z.string().trim().min(1, "Give the name to call it by."),
  base: z.string(),
})

// AddRepo registers a repository: where it is, the name to call it by, and the branch work starts from.
function AddRepo() {
  const refresh = useRefreshRepos()
  const [open, setOpen] = useState(false)

  const add = useMutation({
    mutationFn: (value: z.infer<typeof repoSchema>) =>
      putRepo({
        path: { name: value.name.trim() },
        body: {
          repo: value.repo.trim(),
          ...(value.base.trim() ? { base: value.base.trim() } : {}),
        },
      }),
  })

  const form = useForm({
    defaultValues: { repo: "", name: "", base: "" },
    validators: { onSubmit: repoSchema },
    onSubmit: async ({ value }) => {
      try {
        await add.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset()
      add.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger
        render={
          <Button>
            <Plus data-icon="inline-start" />
            Add repository
          </Button>
        }
      />
      <DialogContent>
        <form
          autoComplete="off"
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <DialogHeader>
            <DialogTitle>Add a repository</DialogTitle>
            <DialogDescription>
              Work can then be given on it by its name instead of its URL.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <form.Field
              name="repo"
              listeners={{
                // The name follows what is typed until the owner writes a name of his own.
                onChange: ({ value }) => {
                  if (!form.getFieldMeta("name")?.isDirty) {
                    form.setFieldValue("name", nameOf(value), { dontUpdateMeta: true })
                  }
                },
              }}
            >
              {(field) => (
                <TextField
                  field={field}
                  label="Repository"
                  placeholder="https://gitlab.company.example/team/app"
                  description="Its URL, or owner/name for one on GitHub. A company's server is added under Integrations first."
                />
              )}
            </form.Field>
            <form.Field name="name">
              {(field) => <TextField field={field} label="Name" />}
            </form.Field>
            <form.Field name="base">
              {(field) => (
                <TextField
                  field={field}
                  label="Base branch"
                  placeholder="The repository's default"
                />
              )}
            </form.Field>
          </FieldGroup>
          <ErrorAlert error={add.error} />
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            <Button type="submit" disabled={add.isPending}>
              {add.isPending && <Spinner data-icon="inline-start" />}
              Add
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
