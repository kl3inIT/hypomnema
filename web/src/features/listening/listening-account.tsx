import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, ClipboardPaste, LogIn, LogOut, ShieldAlert, UserRound } from "lucide-react"
import { useEffect, useId, useState } from "react"

import "@/api/client"
import { getListeningQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import {
  checkListeningSignIn,
  consentListeningSource,
  pasteListeningCookies,
  signOutListeningAccount,
  startListeningSignIn,
} from "@/api/generated/sdk.gen"
import type { Listening, ListeningSource } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
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
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { BrowserView } from "@/features/browser/browser-view"
import { said } from "@/lib/said"
import { since } from "@/lib/time"

import { accountState, signedStep } from "./listening-state"

// The account of a signed-in source (issue #1291), on its card: its consent card before anything, then a sign-in in
// the owner's tab of the shared browser, as the Muse page adds an account (muse-screen.tsx AddAccountDialog), or a
// cookie header pasted instead (last30days-skill-cn's `login --cookie`); once signed in, its state, Sign in again and
// Sign out. The consent's words are the server's, fixed for each source (last30days references/reddit-session.md:
// "reproduce it verbatim"), shown in the risk line the Muse page uses for Meta's terms.

function useShowListening() {
  const queryClient = useQueryClient()

  return (listening: Listening) => queryClient.setQueryData(getListeningQueryKey(), listening)
}

// AccountPanel is the card's account part.
export function AccountPanel({ source }: { source: ListeningSource }) {
  const step = signedStep(source)
  if (!step || !source.signed) return null

  if (step === "consent") {
    return (
      <div className="relative z-20 flex flex-col gap-2 rounded-lg border border-dashed px-3 py-2.5 text-xs">
        <span className="text-muted-foreground">
          Read as one of your accounts. Off until you read what that risks and agree.
        </span>
        <span>
          <ConsentDialog source={source} />
        </span>
      </div>
    )
  }

  const account = source.signed.account
  const state = accountState(source)

  return (
    <section
      aria-label={`${source.label}'s account`}
      className="relative z-20 flex flex-col gap-2 rounded-lg border px-3 py-2.5 text-xs"
    >
      {account && state ? (
        <>
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <UserRound className="size-3.5 text-muted-foreground" aria-hidden />
            <span className="font-medium text-foreground">{account.label}</span>
            <Badge variant={state.tone} title={state.why}>
              {state.label}
            </Badge>
            <span className="text-muted-foreground">Signed in {since(account.signed_in_at)}</span>
          </span>
          {account.cause && account.state !== "signed_in" && (
            <span className="break-words text-muted-foreground">{account.cause}</span>
          )}
        </>
      ) : (
        <span className="text-muted-foreground">
          No account is signed in: its web search reads it meanwhile.
        </span>
      )}
      <span className="flex flex-wrap items-center gap-1.5">
        {source.signed.sign_in_url && <SignInDialog source={source} again={!!account} />}
        <PasteDialog source={source} />
        {account && <SignOut source={source} />}
      </span>
    </section>
  )
}

// ConsentDialog shows the source's consent card; agreeing switches it on.
export function ConsentDialog({
  source,
  trigger = "button",
}: {
  source: ListeningSource
  trigger?: "button" | "switch"
}) {
  const [open, setOpen] = useState(false)
  const show = useShowListening()
  const agree = useMutation({
    mutationFn: async () => (await consentListeningSource({ path: { source: source.name } })).data,
    onSuccess: (listening) => {
      show(listening)
      said(`${source.label} is on: sign its account in`)
      setOpen(false)
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) agree.reset()
      }}
    >
      {trigger === "button" ? (
        <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
          <ShieldAlert data-icon="inline-start" />
          Read the risks and turn on
        </Button>
      ) : (
        <Button variant="ghost" size="sm" onClick={() => setOpen(true)}>
          Turn on
        </Button>
      )}
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Read {source.label} as your account?</DialogTitle>
          <DialogDescription>
            Agreeing turns {source.label} on. You then sign its account in yourself; nothing types
            your password.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <Alert>
            <ShieldAlert aria-hidden />
            <AlertDescription>{source.signed?.consent}</AlertDescription>
          </Alert>
          <ErrorAlert error={agree.error} />
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Not now</DialogClose>
          <Button onClick={() => agree.mutate()} disabled={agree.isPending}>
            {agree.isPending && <Spinner data-icon="inline-start" />}I understand, turn it on
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// SignInDialog opens the site's sign-in page in the owner's tab, shown here, and waits until its session is verified
// by a real search and kept (muse-screen.tsx AddAccountDialog).
function SignInDialog({ source, again }: { source: ListeningSource; again: boolean }) {
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
      await startListeningSignIn({ path: { source: source.name } })
    },
    onSuccess: () => setOpened(true),
  })

  // Every two seconds while the tab is open, one check at a time, until the account is kept.
  useEffect(() => {
    if (!open || !opened || done) return

    let stopped = false
    let busy = false
    const look = async () => {
      if (busy) return
      busy = true
      try {
        const { data } = await checkListeningSignIn({
          path: { source: source.name },
          body: { label: label.trim() || undefined },
        })
        if (stopped) return
        if (!data.done) {
          setWaiting(data.waiting ?? null)
          return
        }
        setDone(true)
        void queryClient.invalidateQueries({ queryKey: getListeningQueryKey() })
        said(`${source.label}'s account is signed in`)
        setTimeout(() => setOpen(false), 1200)
      } catch (err) {
        if (!stopped) setFailed(err)
      } finally {
        busy = false
      }
    }
    const timer = setInterval(() => void look(), 2000)

    return () => {
      stopped = true
      clearInterval(timer)
    }
  }, [open, opened, done, label, source, queryClient])

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
      <Button variant="outline" size="sm" onClick={() => change(true)}>
        <LogIn data-icon="inline-start" />
        {again ? "Sign in again" : "Sign in"}
      </Button>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>Sign {source.label}'s account in</DialogTitle>
          <DialogDescription>
            Sign in to {source.label} in the shared browser below: take control, sign in with the
            secondary account, and give control back. One search then checks the session; its
            cookies go to the vault and leave the shared browser.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <div className="flex flex-col gap-3">
            {!opened && (
              <div className="flex flex-col gap-1.5 text-sm">
                <label htmlFor={labelId} className="font-medium">
                  Name
                </label>
                <Input
                  id={labelId}
                  value={label}
                  maxLength={80}
                  placeholder="secondary"
                  onChange={(event) => setLabel(event.target.value)}
                />
                <span className="text-xs text-muted-foreground">
                  What you call this account here.
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
              Open {source.label}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// PasteDialog signs the account in with a cookie header copied from a browser signed in to the site.
function PasteDialog({ source }: { source: ListeningSource }) {
  const fieldId = useId()
  const [open, setOpen] = useState(false)
  const [cookies, setCookies] = useState("")
  const show = useShowListening()
  const paste = useMutation({
    mutationFn: async () =>
      (
        await pasteListeningCookies({
          path: { source: source.name },
          body: { cookies: cookies.trim() },
        })
      ).data,
    onSuccess: (listening) => {
      show(listening)
      said(`${source.label}'s account is signed in`)
      setOpen(false)
      setCookies("")
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) {
          setCookies("")
          paste.reset()
        }
      }}
    >
      <Button variant="ghost" size="sm" onClick={() => setOpen(true)}>
        <ClipboardPaste data-icon="inline-start" />
        Paste cookies
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Paste {source.label}'s cookies</DialogTitle>
          <DialogDescription>
            In a browser signed in to {source.label} with the secondary account, copy the Cookie
            header of a request to the site (developer tools, Network). It must hold the site's
            sign-in cookies; one search checks it before it is kept in the vault.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <div className="flex flex-col gap-1.5 text-sm">
            <label htmlFor={fieldId} className="font-medium">
              Cookie header
            </label>
            <Textarea
              id={fieldId}
              value={cookies}
              rows={4}
              autoComplete="off"
              spellCheck={false}
              placeholder="name=value; name=value"
              onChange={(event) => setCookies(event.target.value)}
            />
          </div>
          <ErrorAlert error={paste.error} />
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button
            onClick={() => paste.mutate()}
            disabled={paste.isPending || cookies.trim() === ""}
          >
            {paste.isPending && <Spinner data-icon="inline-start" />}
            Check and keep
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function SignOut({ source }: { source: ListeningSource }) {
  const show = useShowListening()

  return (
    <ConfirmDialog
      trigger={
        <Button variant="ghost" size="sm">
          <LogOut data-icon="inline-start" />
          Sign out
        </Button>
      }
      title={`Sign ${source.label}'s account out?`}
      consequence={`Hypomnema removes its cookies from the vault, closes its tab and clears ${source.label}'s cookies from the shared browser, no other site's. ${source.label} stays on, read by its web search.`}
      action="Sign out"
      run={async () => {
        const { data } = await signOutListeningAccount({ path: { source: source.name } })
        show(data)
      }}
      onDone={() => undefined}
      done={`${source.label}'s account is signed out`}
    />
  )
}
