import { Dialog } from "@base-ui/react/dialog"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ExternalLink, GitPullRequestArrow } from "lucide-react"
import { useState, type ReactNode } from "react"

import "@/api/client"
import {
  deleteGitHost,
  deleteGitHostToken,
  putGitHost,
  putGitHostToken,
} from "@/api/generated/sdk.gen"
import type { GitHost, GitHostKind } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import { SettingsPage } from "./settings-shell"
import {
  errorText,
  hostName,
  hostOf,
  hostsKey,
  kindName,
  tokenHelp,
  useHosts,
} from "./use-integrations"

const inputClass = "h-9 rounded-md border border-input bg-background px-3 text-sm"
const popupClass =
  "fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100svh-2rem)] w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto rounded-xl border bg-background p-5 outline-none"
const backdropClass =
  "fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0"

// IntegrationsScreen is where the owner connects the services Hypomnema works with. Today those are the git hosts
// workers push to and open pull requests on; a task tracker would be a second group, as Orca's Integrations pane
// has "Review providers" and "Task providers" (src/renderer/src/components/settings/integrations/IntegrationsPane.tsx).
export function IntegrationsScreen() {
  const hosts = useHosts()

  return (
    <SettingsPage
      title="Integrations"
      description="The services Hypomnema works with, and the access it has to each."
      actions={<AddHost />}
    >
      <section aria-labelledby="git-hosts" className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <h2 id="git-hosts" className="text-sm font-semibold text-foreground">
            Git hosts
          </h2>
          <p className="text-xs text-muted-foreground">
            Where workers clone repositories, push their branches and open pull requests.
          </p>
        </div>
        {hosts.isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
        {hosts.error && (
          <p role="alert" className="text-sm text-destructive">
            {errorText(hosts.error)}
          </p>
        )}
        {hosts.data?.map((host) => (
          <HostCard key={host.host} host={host} />
        ))}
      </section>
    </SettingsPage>
  )
}

type Tone = "connected" | "attention"

const tones: Record<Tone, string> = {
  connected: "border-done/30 bg-done-soft text-done",
  attention: "border-needs-you/30 bg-needs-you-soft text-needs-you",
}

// IntegrationCard is one service: what it is, the access Hypomnema has, the actions, and under a rule what is
// still to do. Adapted from Orca's IntegrationCardShell and IntegrationCardDetails (MIT, Copyright (c) 2026
// Lovecast Inc., https://github.com/stablyai/orca;
// src/renderer/src/components/integrations/presentation/integration-card-shell.tsx).
function IntegrationCard({
  icon,
  name,
  description,
  status,
  tone,
  actions,
  children,
}: {
  icon: ReactNode
  name: string
  description: ReactNode
  status: string
  tone: Tone
  actions?: ReactNode
  children?: ReactNode
}) {
  return (
    <div className="rounded-xl border bg-card p-4" data-testid="integration-card">
      <div className="flex flex-wrap items-start gap-3">
        <span className="shrink-0 text-muted-foreground">{icon}</span>
        <div className="min-w-0 flex-1 basis-64 space-y-0.5">
          <p className="text-sm font-medium">{name}</p>
          <p className="text-xs text-muted-foreground">{description}</p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-1.5 max-sm:basis-full">
          {actions}
          <span
            className={cn(
              "shrink-0 rounded-full border px-2.5 py-1 text-2xs font-medium",
              tones[tone],
            )}
          >
            {status}
          </span>
        </div>
      </div>
      {children && <div className="mt-4 space-y-2 border-t pt-4">{children}</div>}
    </div>
  )
}

const lowerFirst = (text: string) => text.charAt(0).toLowerCase() + text.slice(1)

function HostCard({ host }: { host: GitHost }) {
  const help = tokenHelp(host)

  return (
    <IntegrationCard
      icon={<GitPullRequestArrow className="size-5" />}
      name={hostName(host)}
      description={
        host.known ? `${host.host} · Pull requests` : `${kindName(host)} server · Pull requests`
      }
      status={host.has_token ? "Connected" : "No token"}
      tone={host.has_token ? "connected" : "attention"}
      actions={
        <>
          <SetToken host={host} />
          {host.has_token && <RemoveToken host={host} />}
          {!host.known && <RemoveHost host={host} />}
        </>
      }
    >
      {!host.has_token && (
        <>
          <p className="text-xs text-muted-foreground">
            Without a token, workers use the login of the machine they run on, where it has one.
          </p>
          <p className="text-xs text-muted-foreground">To connect: {lowerFirst(help.needs)}</p>
          <a
            href={help.docs}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 text-xs text-link hover:underline"
          >
            <ExternalLink className="size-3" aria-hidden />
            How to create a token
          </a>
        </>
      )}
    </IntegrationCard>
  )
}

function useRefreshHosts() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: hostsKey })
}

// SetToken is the dialog that takes a host's token, as Orca's credential dialogs take one
// (bitbucket-credentials-dialog.tsx): a password field, what the token needs, and where to make one. The token is
// sent once and never shown again; it lives in this dialog only until it is sent.
function SetToken({ host }: { host: GitHost }) {
  const refresh = useRefreshHosts()
  const [open, setOpen] = useState(false)
  const [token, setToken] = useState("")
  const help = tokenHelp(host)

  const save = useMutation({
    mutationFn: () => putGitHostToken({ path: { host: host.host }, body: { token: token.trim() } }),
    onSuccess: async () => {
      await refresh()
      change(false)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setToken("")
      save.reset()
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={change}>
      <Dialog.Trigger render={<Button variant="outline" size="sm" />}>
        {host.has_token ? "Replace token" : "Add token"}
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className={backdropClass} />
        <Dialog.Popup className={popupClass}>
          <form
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              save.mutate()
            }}
          >
            <Dialog.Title className="text-sm font-medium">
              {host.has_token ? "Replace the token of" : "Add a token for"} {hostName(host)}
            </Dialog.Title>
            <Dialog.Description className="text-sm text-muted-foreground">
              {help.needs} It is kept encrypted on the server and is not shown again.
            </Dialog.Description>
            <label className="flex flex-col gap-1.5 text-sm">
              Access token
              <input
                required
                type="password"
                autoComplete="off"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                className={inputClass}
              />
            </label>
            <a
              href={help.docs}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-link hover:underline"
            >
              <ExternalLink className="size-3" aria-hidden />
              How to create a token
            </a>
            {save.error && (
              <p role="alert" className="text-sm text-destructive">
                {errorText(save.error)}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
              <Button type="submit" disabled={save.isPending || token.trim() === ""}>
                {save.isPending ? "Saving…" : "Save token"}
              </Button>
            </div>
          </form>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

// Confirm asks before something is taken away, and says what follows from it.
function Confirm({
  trigger,
  title,
  consequence,
  action,
  run,
}: {
  trigger: string
  title: string
  consequence: string
  action: string
  run: () => Promise<unknown>
}) {
  const refresh = useRefreshHosts()
  const [open, setOpen] = useState(false)
  const remove = useMutation({
    mutationFn: run,
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
      <Dialog.Trigger render={<Button variant="ghost" size="sm" />}>{trigger}</Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className={backdropClass} />
        <Dialog.Popup className={popupClass}>
          <Dialog.Title className="text-sm font-medium">{title}</Dialog.Title>
          <Dialog.Description className="text-sm text-muted-foreground">
            {consequence}
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
              {action}
            </Button>
          </div>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function RemoveToken({ host }: { host: GitHost }) {
  return (
    <Confirm
      trigger="Remove token"
      title={`Remove the token of ${hostName(host)}?`}
      consequence="Workers will use the login of the machine they run on, where it has one. Work already running keeps the token it started with."
      action="Remove token"
      run={() => deleteGitHostToken({ path: { host: host.host } })}
    />
  )
}

function RemoveHost({ host }: { host: GitHost }) {
  return (
    <Confirm
      trigger="Remove"
      title={`Remove ${host.host}?`}
      consequence="Its token is removed with it. A host that an open Run or a registered repository is on cannot be removed."
      action="Remove host"
      run={() => deleteGitHost({ path: { host: host.host } })}
    />
  )
}

// AddHost declares a company's git server: its address, what it runs, and its token if the owner has it at hand.
function AddHost() {
  const refresh = useRefreshHosts()
  const [open, setOpen] = useState(false)
  const [address, setAddress] = useState("")
  const [kind, setKind] = useState<GitHostKind>("gitlab")
  const [token, setToken] = useState("")
  const host = hostOf(address)

  const add = useMutation({
    mutationFn: async () => {
      await putGitHost({ path: { host }, body: { kind } })
      if (token.trim() !== "") {
        await putGitHostToken({ path: { host }, body: { token: token.trim() } })
      }
    },
    // The host may have been declared even when its token was refused.
    onSettled: () => refresh(),
    onSuccess: () => change(false),
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setAddress("")
      setKind("gitlab")
      setToken("")
      add.reset()
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={change}>
      <Dialog.Trigger render={<Button />}>Add a git server</Dialog.Trigger>
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
            <Dialog.Title className="text-sm font-medium">Add a git server</Dialog.Title>
            <Dialog.Description className="text-sm text-muted-foreground">
              A company&apos;s own GitLab or GitHub server. github.com and gitlab.com are already
              here.
            </Dialog.Description>
            <div className="flex flex-col gap-1.5 text-sm">
              <label htmlFor="host-address">Address</label>
              <input
                id="host-address"
                required
                autoComplete="off"
                aria-describedby="host-address-hint"
                placeholder="gitlab.company.example"
                value={address}
                onChange={(event) => setAddress(event.target.value)}
                className={inputClass}
              />
              <span id="host-address-hint" className="text-xs text-muted-foreground">
                Its name, or any URL copied from it.
              </span>
            </div>
            <label className="flex flex-col gap-1.5 text-sm">
              It runs
              <select
                value={kind}
                onChange={(event) => setKind(event.target.value as GitHostKind)}
                className={inputClass}
              >
                <option value="gitlab">GitLab</option>
                <option value="github">GitHub Enterprise</option>
              </select>
            </label>
            <label className="flex flex-col gap-1.5 text-sm">
              Access token
              <input
                type="password"
                autoComplete="off"
                placeholder="Optional; it can be added later"
                value={token}
                onChange={(event) => setToken(event.target.value)}
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
              <Button type="submit" disabled={add.isPending || host === ""}>
                {add.isPending ? "Adding…" : "Add"}
              </Button>
            </div>
          </form>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
