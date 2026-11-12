import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { Copy, ExternalLink, RotateCcw } from "lucide-react"
import { useState, type ReactElement } from "react"
import { z } from "zod"

import "@/api/client"
import { addTokenAccount, beginAccountSignIn, completeAccountSignIn } from "@/api/generated/sdk.gen"
import type { Account, AccountSignInAttempt } from "@/api/generated/types.gen"
import { Button, buttonVariants } from "@/components/ui/button"
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
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ErrorAlert, TextField } from "@/features/settings/settings-parts"
import { said } from "@/features/settings/use-integrations"

import { useRefreshAccounts } from "./use-models"

export type SignInRuntime = "claude" | "codex" | "antigravity"

type Way = "sign-in" | "token"

const finishSchema = z.object({
  callback: z.string().trim().min(1, "Paste what the page gave you."),
})

const tokenSchema = z.object({
  label: z.string().trim().min(1, "Give the account a name.").max(64, "At most 64 characters."),
  token: z
    .string()
    .trim()
    .min(1, "Paste the token.")
    .refine((typed) => !/\s/.test(typed), "The token is one line without spaces."),
})

// AccountSignInDialog adds a subscription account on the control plane, or signs one in again (issue #199). It
// has no step before the sign-in: opening it starts one, as 9router's dialog does (OAuthModal.js), so the link
// and the field for what the page gives back are there at once. The account takes its e-mail as its name, as
// 9router names a connection. Claude has a second way beside it, as 9router has "Paste token" beside its browser
// sign-in and OpenClaw has `models auth paste-token`: the token `claude setup-token` prints, which lasts a year.
// Nothing is opened on the server, and no password passes through Hypomnema.
export function AccountSignInDialog({
  runtime,
  name,
  account,
  trigger,
}: {
  runtime: SignInRuntime
  // name is the subscription as its vendor calls it, such as Claude.
  name: string
  // account is the account signed in again; a new one when absent.
  account?: Account
  trigger: ReactElement
}) {
  const refresh = useRefreshAccounts()
  const [open, setOpen] = useState(false)
  const [way, setWay] = useState<Way>("sign-in")
  const [started, setStarted] = useState<AccountSignInAttempt | null>(null)
  // A token is pasted only for a new Claude account: a signed-in account is signed in again the same way.
  const pastable = runtime === "claude" && !account

  async function finished(signedIn: Account) {
    await refresh()
    change(false)
    said(`${name} account ${signedIn.label} is signed in`)
  }

  const complete = useMutation({
    mutationFn: async (input: { id: string; callback?: string }) =>
      (
        await completeAccountSignIn({
          path: { sign_in_id: input.id },
          body: input.callback ? { callback: input.callback.trim() } : {},
        })
      ).data,
    onSuccess: finished,
  })

  const begin = useMutation({
    mutationFn: async () => (await beginAccountSignIn({ body: { runtime } })).data,
    // Every vendor signs in here with the browser, as 9router, CLIProxyAPI and T3 Code do on the web; Codex's
    // device code is for `accounts login` in a terminal.
    onSuccess: setStarted,
  })

  const addToken = useMutation({
    mutationFn: async (input: { label: string; token: string }) =>
      (
        await addTokenAccount({
          body: { runtime: "claude", label: input.label, token: input.token },
        })
      ).data,
    onSuccess: finished,
  })

  const finish = useForm({
    defaultValues: { callback: "" },
    validators: { onSubmit: finishSchema },
    onSubmit: ({ value }) => {
      if (started) complete.mutate({ id: started.id, callback: value.callback })
    },
  })

  const token = useForm({
    defaultValues: { label: "", token: "" },
    validators: { onSubmit: tokenSchema },
    onSubmit: ({ value }) =>
      addToken.mutate({ label: value.label.trim(), token: value.token.trim() }),
  })

  // startAgain begins a new sign-in: one is completed once, so after a refused one the owner opens a new link.
  function startAgain() {
    setStarted(null)
    finish.reset()
    complete.reset()
    begin.mutate()
  }

  function change(next: boolean) {
    setOpen(next)
    if (next) {
      begin.mutate()
      return
    }

    setWay("sign-in")
    setStarted(null)
    finish.reset()
    token.reset()
    begin.reset()
    complete.reset()
    addToken.reset()
  }

  const minutes = started ? Math.round(started.expires_in_seconds / 60) : 0
  // A pasted answer is used once: after a refusal only a new link can complete the sign-in.
  const spent = complete.isError || begin.isError
  const busy = complete.isPending || addToken.isPending

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <form
          autoComplete="off"
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            void (way === "token" ? token.handleSubmit() : finish.handleSubmit())
          }}
        >
          <DialogHeader>
            <DialogTitle>
              {account ? `Sign ${account.label} in again` : `Add a ${name} account`}
            </DialogTitle>
            <DialogDescription>
              {way === "token"
                ? "On any machine with Claude Code, run claude setup-token and paste the token it prints. It lasts a year; it calls models but may not show the account's limits."
                : `Sign in on ${name}'s own page, in a browser on any machine. Nothing is opened on the server, and your password does not pass through Hypomnema.`}
            </DialogDescription>
          </DialogHeader>

          {pastable && (
            <Tabs value={way} onValueChange={(next) => setWay(next as Way)}>
              <TabsList>
                <TabsTrigger value="sign-in">Sign in</TabsTrigger>
                <TabsTrigger value="token">Paste a token</TabsTrigger>
              </TabsList>
            </Tabs>
          )}

          {way === "sign-in" && (
            <FieldGroup>
              {begin.isPending && (
                <p className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Spinner />
                  Getting the sign-in link…
                </p>
              )}

              {started && (
                <Field>
                  <FieldLabel>1. Sign in on {name}&apos;s page</FieldLabel>
                  <div className="flex gap-2">
                    {/* A link that looks like a button: it leaves this page, so it is an anchor, not a button. */}
                    <a
                      href={started.url}
                      target="_blank"
                      rel="noreferrer"
                      className={buttonVariants({ variant: "outline", className: "flex-1" })}
                    >
                      <ExternalLink data-icon="inline-start" />
                      Open {name}&apos;s sign-in page
                    </a>
                    <Button
                      type="button"
                      variant="ghost"
                      onClick={() => {
                        void navigator.clipboard.writeText(started.url)
                        said("The link was copied")
                      }}
                    >
                      <Copy data-icon="inline-start" />
                      Copy link
                    </Button>
                  </div>
                </Field>
              )}

              {started && (
                <finish.Field name="callback">
                  {(field) => (
                    <TextField
                      field={field}
                      label="2. Paste what the page gives back"
                      placeholder={
                        runtime === "claude"
                          ? "The code the page shows"
                          : "http://localhost:…/callback?code=…"
                      }
                      description={
                        runtime === "claude"
                          ? `${name} shows a code once you sign in: copy it. Within ${minutes} minutes.`
                          : `The browser ends on an address that does not load: copy the whole address. Within ${minutes} minutes.`
                      }
                    />
                  )}
                </finish.Field>
              )}
            </FieldGroup>
          )}

          {way === "token" && (
            <FieldGroup>
              <token.Field name="label">
                {(field) => (
                  <TextField
                    field={field}
                    label="Name"
                    placeholder="personal"
                    description="What you call this account: a token names no e-mail."
                  />
                )}
              </token.Field>
              <token.Field name="token">
                {(field) => (
                  <TextField field={field} label="Token" placeholder="sk-ant-oat01-…" secret />
                )}
              </token.Field>
            </FieldGroup>
          )}

          <ErrorAlert error={way === "token" ? addToken.error : (begin.error ?? complete.error)} />
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            {way === "sign-in" && spent && (
              <Button type="button" onClick={startAgain}>
                <RotateCcw data-icon="inline-start" />
                Start again
              </Button>
            )}
            {way === "sign-in" && !spent && started && (
              <Button type="submit" disabled={busy}>
                {busy && <Spinner data-icon="inline-start" />}
                Sign in
              </Button>
            )}
            {way === "token" && (
              <Button type="submit" disabled={busy}>
                {busy && <Spinner data-icon="inline-start" />}
                Add account
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
