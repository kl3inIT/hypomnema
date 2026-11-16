import { useForm, type AnyFieldApi } from "@tanstack/react-form"
import { useMutation, useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ExternalLink, FolderGit2, KeyRound, Plus } from "lucide-react"
import { useId, useState } from "react"

import "@/api/client"
import { deleteRepo, putRepo } from "@/api/generated/sdk.gen"
import type { GitHost, Repo } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
} from "@/components/ui/code-block"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
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
import { Textarea } from "@/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { TextField } from "@/components/text-field"
import { SettingsPage, SettingsSection } from "@/components/settings-page"
import { said } from "@/lib/said"
import {
  bodyOf,
  fingerprints,
  formOf,
  repoSchema,
  sshHostOf,
  sshKeysPage,
  type RepoForm,
} from "./repositories"
import { nameOf, useGitSSHKey, useHosts, useRefreshRepos, useRepos } from "./use-integrations"

// RepositoriesScreen lists the repositories registered under a name, which a Run can then be started on by that
// name: a list of rows, each with its actions, as t3code's and Orca's settings list their projects and repositories
// (t3code apps/web/src/components/settings/ProjectsSettings.tsx, Orca
// src/renderer/src/components/settings/repository/RepositoryPane.tsx, which edits a repository in Settings). The
// control plane's SSH key heads the page, since only a repository cloned over SSH uses it (issue #647). host is
// the git host the list is narrowed to, kept in the URL so that Integrations can link to it; onHost changes it.
export function RepositoriesScreen({
  host,
  onHost,
}: {
  host?: string
  onHost: (host: string | undefined) => void
}) {
  const repos = useRepos()
  const shown = repos.data?.filter((repo) => !host || repo.host === host)

  return (
    <SettingsPage
      title="Repositories"
      description="Repositories registered under a short name, so that work can be given by that name."
      actions={<RepoDialog />}
    >
      <SSHKeySection repos={repos.data} />
      <SettingsSection
        id="registered-repositories"
        icon={FolderGit2}
        title="Repositories"
        note="Each is cloned over HTTPS with its host's token, or over SSH with the key above."
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
                A Run can always name a repository by its URL. Register the ones you use often to
                call them by a short name.
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
        {repos.data && repos.data.length > 0 && (
          <HostFilter repos={repos.data} host={host} onHost={onHost} />
        )}
        {shown && shown.length > 0 && (
          <ItemGroup>
            {shown.map((repo) => (
              <RepoRow key={repo.name} repo={repo} />
            ))}
          </ItemGroup>
        )}
        {host && repos.data && repos.data.length > 0 && shown?.length === 0 && (
          <p className="text-sm text-muted-foreground">No repository is registered on {host}.</p>
        )}
      </SettingsSection>
    </SettingsPage>
  )
}

// HostFilter narrows the list to one git host, a pill a host with how many repositories it has, as the Usage
// page's period is chosen (usage-screen.tsx). It shows once there is more than one host to choose from, or a host
// is chosen.
function HostFilter({
  repos,
  host,
  onHost,
}: {
  repos: Repo[]
  host?: string
  onHost: (host: string | undefined) => void
}) {
  const counts = new Map<string, number>()
  for (const repo of repos) counts.set(repo.host, (counts.get(repo.host) ?? 0) + 1)
  if (counts.size < 2 && !host) return null

  const chip = (label: string, count: number) => (
    <>
      {label} <span className="text-muted-foreground tabular-nums">{count}</span>
    </>
  )

  return (
    <ToggleGroup
      aria-label="Git host"
      value={[host ?? "all"]}
      onValueChange={(next: string[]) => next[0] && onHost(next[0] === "all" ? undefined : next[0])}
      variant="choice"
      size="sm"
      spacing={1}
    >
      <ToggleGroupItem value="all">{chip("All", repos.length)}</ToggleGroupItem>
      {[...counts].map(([name, count]) => (
        <ToggleGroupItem key={name} value={name}>
          {chip(name, count)}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  )
}

// SSHKeySection shows the public half of the control plane's SSH key (issue #625), which nodes clone a repository
// registered with an SSH address with: one line to copy into the git host's SSH keys, as Coder's SSH keys page shows
// a user's key with a copy button (site/src/pages/UserSettingsPage/SSHKeysPage/SSHKeysPageView.tsx), and the hosts
// whose repositories are cloned with it, each with a link to the page of that host where a key is added. The key is
// made on the first read; its private half never leaves the server.
function SSHKeySection({ repos }: { repos?: Repo[] }) {
  const key = useGitSSHKey()
  const hosts = useHosts()

  const used = new Map<string, number>()
  for (const repo of repos ?? []) {
    if (repo.ssh_url) used.set(repo.host, (used.get(repo.host) ?? 0) + 1)
  }

  return (
    <SettingsSection
      id="ssh-key"
      icon={KeyRound}
      title="SSH key"
      note="For repositories cloned over SSH. Add it once to the account that clones, under SSH keys on the git host; the private key stays on the server."
    >
      <div className="flex flex-col gap-2" data-testid="ssh-key">
        {key.isPending && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner />
            Loading…
          </p>
        )}
        <ErrorAlert error={key.error} />
        {key.data && (
          <CodeBlock size="sm">
            <CodeBlockActions>
              <CodeBlockCopyButton value={key.data.public_key} />
            </CodeBlockActions>
            <CodeBlockContent wrap>
              <span className="break-all">{key.data.public_key}</span>
            </CodeBlockContent>
          </CodeBlock>
        )}
        {used.size > 0 && (
          <ItemGroup>
            {[...used].map(([name, count]) => (
              <Item key={name} variant="muted" size="sm" aria-label={`SSH host ${name}`}>
                <ItemContent>
                  <ItemTitle>{name}</ItemTitle>
                  <ItemDescription>
                    Used by {count} {count === 1 ? "repository" : "repositories"}.
                  </ItemDescription>
                </ItemContent>
                <ItemActions>
                  <a
                    href={sshKeysPage(name, kindOf(name, hosts.data))}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 text-xs text-link hover:underline"
                  >
                    SSH keys on the host
                    <ExternalLink className="size-3" aria-hidden />
                  </a>
                </ItemActions>
              </Item>
            ))}
          </ItemGroup>
        )}
      </div>
    </SettingsSection>
  )
}

// kindOf is what a host runs, as Integrations has it; github.com runs GitHub before the hosts are read.
function kindOf(host: string, hosts?: GitHost[]) {
  return hosts?.find((h) => h.host === host)?.kind ?? (host === "github.com" ? "github" : "gitlab")
}

// RepoRow is one repository: its name, how it is cloned, where it is (its host leading to Integrations, where the
// host's token is), the branch work starts from, and what the owner can do with it.
function RepoRow({ repo }: { repo: Repo }) {
  const refresh = useRefreshRepos()

  return (
    <Item variant="outline">
      <ItemMedia variant="icon">
        <FolderGit2 />
      </ItemMedia>
      <ItemContent>
        <ItemTitle>
          {repo.name}
          <Badge variant="outline">{repo.ssh_url ? "SSH" : "HTTPS"}</Badge>
        </ItemTitle>
        <ItemDescription>
          <Link to="/settings/integrations" className="text-link hover:underline">
            {repo.host}
          </Link>
          /{repo.path}
          {repo.base ? ` · starts from ${repo.base}` : ""}
          {repo.ssh_url ? ` · cloned over SSH from ${sshHostOf(repo.ssh_url)}` : ""}
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        <RepoDialog repo={repo} />
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
          done={`${repo.name} was removed`}
        />
      </ItemActions>
    </Item>
  )
}

// RepoDialog registers a repository, or with repo changes one (issue #647): where it is and the name to call it by
// when it is added, the branch work starts from, and whether workers clone it over HTTPS or, when its host's HTTPS
// cannot carry a large clone, over SSH from an address with the host's key (issue #625). One dialog for both, as
// the Models page's Edit opens the dialog its fields are set in.
function RepoDialog({ repo }: { repo?: Repo }) {
  const formId = useId()
  const refresh = useRefreshRepos()
  const [open, setOpen] = useState(false)

  const save = useMutation({
    mutationFn: (value: RepoForm) =>
      putRepo({ path: { name: value.name.trim() }, body: bodyOf(value, repo?.connection) }),
  })

  const form = useForm({
    defaultValues: formOf(repo),
    validators: { onSubmit: repoSchema },
    onSubmit: async ({ value }) => {
      try {
        await save.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
      said(`${value.name.trim()} was ${repo ? "saved" : "added"}`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset(formOf(repo))
      save.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger
        render={
          repo ? (
            <Button variant="ghost" size="sm" aria-label={`Edit ${repo.name}`}>
              Edit
            </Button>
          ) : (
            <Button>
              <Plus data-icon="inline-start" />
              Add repository
            </Button>
          )
        }
      />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{repo ? `Edit ${repo.name}` : "Add a repository"}</DialogTitle>
          <DialogDescription>
            {repo
              ? `${repo.host}/${repo.path}`
              : "Work can then be given on it by its name instead of its URL."}
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            autoComplete="off"
            noValidate
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              void form.handleSubmit()
            }}
          >
            <FieldGroup>
              {!repo && (
                <>
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
                </>
              )}
              <form.Field name="base">
                {(field) => (
                  <TextField
                    field={field}
                    label="Base branch"
                    placeholder="The repository's default"
                  />
                )}
              </form.Field>
              <form.Field name="over">
                {(field) => (
                  <Field>
                    <FieldLabel>Clone over</FieldLabel>
                    <ToggleGroup
                      aria-label="Clone over"
                      value={[field.state.value]}
                      onValueChange={(next: string[]) =>
                        next[0] && field.handleChange(next[0] as RepoForm["over"])
                      }
                      variant="choice"
                      size="sm"
                      spacing={1}
                    >
                      <ToggleGroupItem value="https">HTTPS</ToggleGroupItem>
                      <ToggleGroupItem value="ssh">SSH</ToggleGroupItem>
                    </ToggleGroup>
                    <FieldDescription>
                      HTTPS with the host's token; SSH for a host whose HTTPS cannot carry a large
                      repository.
                    </FieldDescription>
                  </Field>
                )}
              </form.Field>
              <form.Subscribe selector={(state) => state.values.over}>
                {(over) =>
                  over === "ssh" && (
                    <>
                      <form.Field name="sshUrl">
                        {(field) => (
                          <TextField
                            field={field}
                            label="SSH address"
                            placeholder="ssh://git@git.company.example:222/team/app.git"
                            description="As the host shows it under Clone with SSH. Workers clone it with the SSH key at the top of this page."
                          />
                        )}
                      </form.Field>
                      <form.Field name="sshHostKey">
                        {(field) => <HostKeyField field={field} />}
                      </form.Field>
                    </>
                  )
                }
              </form.Subscribe>
            </FieldGroup>
            <ErrorAlert error={save.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button
            type="submit"
            form={formId}
            focusableWhenDisabled={save.isPending}
            disabled={save.isPending}
          >
            {save.isPending && <Spinner data-icon="inline-start" />}
            {repo ? "Save" : "Add"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// HostKeyField takes the SSH host's keys, as ssh-keyscan prints them, a line a key, and shows each key's SHA256
// fingerprint to check against the host's own, as the CLI prints them for `repos set`.
function HostKeyField({ field }: { field: AnyFieldApi }) {
  const invalid = field.state.meta.isTouched && !field.state.meta.isValid
  const prints = useQuery({
    queryKey: ["ssh-host-key-fingerprints", field.state.value],
    queryFn: () => fingerprints(field.state.value as string),
    staleTime: Infinity,
  })

  return (
    <Field data-invalid={invalid || undefined}>
      <FieldLabel htmlFor={field.name}>SSH host key</FieldLabel>
      <Textarea
        id={field.name}
        name={field.name}
        code
        rows={2}
        placeholder="ssh-ed25519 AAAA…"
        value={field.state.value as string}
        aria-invalid={invalid || undefined}
        onBlur={field.handleBlur}
        onChange={(event) => field.handleChange(event.target.value)}
      />
      {prints.data?.map((print) => (
        <p
          key={print.fingerprint}
          className="text-xs break-all text-muted-foreground"
          data-testid="host-key-fingerprint"
        >
          Fingerprint <span className="font-mono text-foreground">{print.fingerprint}</span> (
          {print.type})
        </p>
      ))}
      <FieldDescription>
        The host's key, as ssh-keyscan -p &lt;port&gt; &lt;host&gt; prints it; check its fingerprint
        against the host's own. Workers trust the host under this key alone.
      </FieldDescription>
      {invalid && <FieldError errors={field.state.meta.errors} />}
    </Field>
  )
}
