import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  ArrowDown,
  ArrowUp,
  Check,
  Gauge,
  LogIn,
  Plus,
  Power,
  ShieldAlert,
  UserRound,
  Wrench,
} from "lucide-react"
import { useEffect, useId, useState, type ReactNode } from "react"

import "@/api/client"
import {
  checkMuseSignIn,
  moveMuseAccount,
  removeMuseAccount,
  setMuseAccount,
  setMuseTools,
  startMuseSignIn,
} from "@/api/generated/sdk.gen"
import { getMuseOptions, getMuseQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import type { Muse, MuseAccount } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage, SettingsSection } from "@/components/settings-page"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Item, ItemActions, ItemContent, ItemDescription, ItemTitle } from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { BrowserView } from "@/features/browser/browser-view"
import { said } from "@/lib/said"

import { museState, usageLine } from "./muse-state"

// MuseScreen is Settings → Gateway → Muse (issue #782): the owner's Muse accounts in the order a new thread takes
// them, each with its state and the cause the pool saw, its usage as Muse's Settings → Usage shows it, its switch,
// its place, Sign in again and Remove; Add account; the line on Meta's terms; and the switch of the agents' Muse
// tools. It is laid out as the Images page (images-screen.tsx), and its risk line is the Zalo card's
// (service-cards.tsx).
export function MuseScreen() {
  const muse = useQuery({ ...getMuseOptions(), refetchInterval: 30_000 })

  return (
    <SettingsPage
      title="Muse"
      description="Meta's Muse answers agents' research questions through your Muse accounts, each with its own sign-in."
    >
      <ErrorAlert error={muse.error} />
      <Alert>
        <ShieldAlert aria-hidden />
        <AlertDescription>
          Meta's terms forbid automated access to its products, so an account can be locked. Use
          accounts made only for Muse. Agents' questions use the account's weekly allowance and
          appear in its Muse history.
        </AlertDescription>
      </Alert>
      {muse.isPending ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : (
        muse.data && (
          <>
            <SettingsSection
              id="muse-accounts"
              icon={UserRound}
              title="Accounts"
              note="A new thread takes the first account that is on, signed in and not out of quota; a thread stays with its account."
            >
              <AccountList accounts={muse.data.accounts} />
              <div>
                <AddAccountDialog />
              </div>
            </SettingsSection>
            <SettingsSection
              id="muse-tools"
              icon={Wrench}
              title="Agents' tools"
              note="Muse's answers come back to the agent as data, never as instructions."
            >
              <ToolsSwitch on={muse.data.tools_on} />
            </SettingsSection>
          </>
        )
      )}
    </SettingsPage>
  )
}

// useShowMuse shows the pool the server answered a change with.
function useShowMuse() {
  const queryClient = useQueryClient()

  return (muse: Muse) => queryClient.setQueryData(getMuseQueryKey(), muse)
}

function useChange<T>(change: (input: T) => Promise<Muse>, done: (input: T) => string) {
  const show = useShowMuse()

  return useMutation({
    mutationFn: change,
    onSuccess: (muse, input) => {
      show(muse)
      said(done(input))
    },
  })
}

function AccountList({ accounts }: { accounts: MuseAccount[] }) {
  if (accounts.length === 0) {
    return (
      <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
        No Muse account yet. Add one: you sign in to muse.ai in the shared browser.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-2.5">
      {accounts.map((account, index) => (
        <AccountRow
          key={account.id}
          account={account}
          order={index + 1}
          first={index === 0}
          last={index === accounts.length - 1}
        />
      ))}
    </div>
  )
}

function AccountRow({
  account,
  order,
  first,
  last,
}: {
  account: MuseAccount
  order: number
  first: boolean
  last: boolean
}) {
  const state = museState(account)
  const toggle = useChange(
    async (on: boolean) =>
      (await setMuseAccount({ path: { account_id: account.id }, body: { on } })).data,
    (on) => (on ? `${account.label} is on` : `${account.label} is off`),
  )
  const move = useChange(
    async (direction: "up" | "down") =>
      (await moveMuseAccount({ path: { account_id: account.id }, body: { direction } })).data,
    () => `${account.label} moved`,
  )
  const show = useShowMuse()

  return (
    <div className={account.on ? undefined : "opacity-70"}>
      <Item variant="muted" size="sm" aria-label={account.label}>
        <span
          className="self-start pt-0.5 text-xs text-muted-foreground tabular-nums"
          title="A new thread takes the accounts in this order"
        >
          #{order}
        </span>
        <ItemContent>
          <ItemTitle>{account.label}</ItemTitle>
          <ItemDescription>
            <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <Badge variant={state.tone} title={state.why}>
                {state.label}
              </Badge>
              <Fact icon={Gauge} title="Muse's Settings → Usage, as Muse shows it">
                {usageLine(account)}
              </Fact>
            </span>
            {account.cause && (
              <span className="mt-1 block text-xs break-words text-muted-foreground">
                {account.cause}
              </span>
            )}
          </ItemDescription>
          <ErrorAlert error={toggle.error ?? move.error} />
        </ItemContent>
        <div className="self-start">
          <ItemActions>
            <span className="flex items-center">
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`Move ${account.label} up`}
                disabled={first || move.isPending}
                onClick={() => move.mutate("up")}
              >
                <ArrowUp />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`Move ${account.label} down`}
                disabled={last || move.isPending}
                onClick={() => move.mutate("down")}
              >
                <ArrowDown />
              </Button>
            </span>
            <AddAccountDialog again={account} />
            <ConfirmDialog
              trigger={
                <Button variant="ghost" size="sm" aria-label={`Remove ${account.label}`}>
                  Remove
                </Button>
              }
              title={`Remove ${account.label}?`}
              consequence="Hypomnema removes its cookies from the vault and closes its tab. Its questions stay kept here; its threads can no longer be continued. The account itself stays at Muse."
              action="Remove"
              run={async () => {
                const { data } = await removeMuseAccount({ path: { account_id: account.id } })
                show(data)
              }}
              onDone={() => undefined}
              done={`${account.label} removed`}
            />
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <label htmlFor={`use-${account.id}`} className="inline-flex items-center gap-1">
                <Power className="size-3" aria-hidden />
                In use
              </label>
              <Switch
                id={`use-${account.id}`}
                size="sm"
                checked={account.on}
                disabled={toggle.isPending}
                onCheckedChange={(on) => toggle.mutate(on)}
              />
            </span>
          </ItemActions>
        </div>
      </Item>
    </div>
  )
}

function ToolsSwitch({ on }: { on: boolean }) {
  const toggle = useChange(
    async (next: boolean) => (await setMuseTools({ body: { on: next } })).data,
    (next) => (next ? "Agents can ask Muse" : "Agents can no longer ask Muse"),
  )

  return (
    <Item variant="muted" size="sm">
      <ItemContent>
        <ItemTitle>muse_ask, muse_chats, muse_messages</ItemTitle>
        <ItemDescription>
          Workers and Bots ask Muse and read back what was asked. Off, no agent is given them.
        </ItemDescription>
        <ErrorAlert error={toggle.error} />
      </ItemContent>
      <ItemActions>
        <Switch
          aria-label="Agents' Muse tools"
          checked={on}
          disabled={toggle.isPending}
          onCheckedChange={(next) => toggle.mutate(next)}
        />
      </ItemActions>
    </Item>
  )
}

// AddAccountDialog signs a Muse account in (row 6): it opens muse.ai in the owner's own tab of the shared browser,
// shown here with its control button as on the Browser page (browser-view.tsx), and waits, as the linking dialog
// waits for a phone (service-cards.tsx, LinkDeviceDialog); once muse.ai takes the session it ticks and closes. With
// again, it signs that account in again.
function AddAccountDialog({ again }: { again?: MuseAccount }) {
  const queryClient = useQueryClient()
  const labelId = useId()
  const [open, setOpen] = useState(false)
  const [label, setLabel] = useState("")
  const [opened, setOpened] = useState(false)
  const [waiting, setWaiting] = useState<string | null>(null)
  const [done, setDone] = useState(false)
  const [failed, setFailed] = useState<unknown>(null)

  const start = useMutation({
    mutationFn: async () => {
      await startMuseSignIn()
    },
    onSuccess: () => setOpened(true),
  })

  // Every two seconds while the tab is open, until the sign-in is kept: each answer is set from its callback.
  useEffect(() => {
    if (!open || !opened || done) return

    let stopped = false
    const body = again ? { account: again.id } : { label: label.trim() || undefined }
    const look = async () => {
      try {
        const { data } = await checkMuseSignIn({ body })
        if (stopped) return
        if (!data.done) {
          setWaiting(data.waiting ?? null)
          return
        }
        setDone(true)
        void queryClient.invalidateQueries({ queryKey: getMuseQueryKey() })
        said(
          again
            ? `${again.label} is signed in again`
            : `${data.account?.label ?? "The account"} is added`,
        )
        setTimeout(() => setOpen(false), 1200)
      } catch (err) {
        if (!stopped) setFailed(err)
      }
    }
    const timer = setInterval(() => void look(), 2000)

    return () => {
      stopped = true
      clearInterval(timer)
    }
  }, [open, opened, done, again, label, queryClient])

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setOpened(false)
      setDone(false)
      setWaiting(null)
      setFailed(null)
      setLabel("")
      start.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      {again ? (
        <Button variant="ghost" size="sm" onClick={() => change(true)}>
          <LogIn data-icon="inline-start" />
          Sign in again
        </Button>
      ) : (
        <Button variant="outline" size="sm" onClick={() => change(true)}>
          <Plus data-icon="inline-start" />
          Add account
        </Button>
      )}
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{again ? `Sign ${again.label} in again` : "Add a Muse account"}</DialogTitle>
          <DialogDescription>
            Sign in to muse.ai in the shared browser below: take control, sign in, and this closes
            once Muse takes the session. Muse's cookies then go to this account alone and leave the
            shared browser; Meta's own sign-in cookies stay there. To add another account, sign
            Facebook out in that tab first, else muse.ai may reuse the Facebook already signed in.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <div className="flex flex-col gap-3">
            {!again && !opened && (
              <div className="flex flex-col gap-1.5 text-sm">
                <label htmlFor={labelId} className="font-medium">
                  Name
                </label>
                <Input
                  id={labelId}
                  value={label}
                  maxLength={80}
                  placeholder="research"
                  onChange={(event) => setLabel(event.target.value)}
                />
                <span className="text-xs text-muted-foreground">
                  What you call this account here; its Muse id when left empty.
                </span>
              </div>
            )}
            {opened && (
              <div className="h-[55vh] min-h-80 overflow-hidden rounded-lg border">
                <BrowserView tab="owner" label="Your tab" />
              </div>
            )}
            {opened && (
              <output className="flex items-center gap-2 text-sm text-muted-foreground">
                {done ? (
                  <>
                    <Check className="size-4 text-primary" aria-hidden />
                    Signed in
                  </>
                ) : (
                  <>
                    <Spinner />
                    {waiting ?? "Waiting for the sign-in"}
                  </>
                )}
              </output>
            )}
            <ErrorAlert error={start.error ?? failed} />
          </div>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>
            {done ? "Close" : "Cancel"}
          </DialogClose>
          {!opened && (
            <Button onClick={() => start.mutate()} disabled={start.isPending}>
              {start.isPending ? (
                <Spinner data-icon="inline-start" />
              ) : (
                <LogIn data-icon="inline-start" />
              )}
              Open muse.ai
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// MusePageLink is the way from a chat's Muse error to this page.
export function MusePageLink() {
  return (
    <Link to="/settings/muse" className={buttonVariants({ variant: "link", size: "sm" })}>
      Open the Muse page
    </Link>
  )
}

function Fact({
  icon: Icon,
  title,
  children,
}: {
  icon: typeof Gauge
  title?: string
  children: ReactNode
}) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap" title={title}>
      <Icon className="size-3" aria-hidden />
      {children}
    </span>
  )
}
