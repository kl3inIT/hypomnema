import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { Copy, ExternalLink, FileUp, RotateCcw } from "lucide-react"
import { useId, useRef, useState, type ReactElement } from "react"
import { z } from "zod"

import "@/api/client"
import {
  addTokenAccount,
  beginAccountSignIn,
  completeAccountSignIn,
  importAccount,
} from "@/api/generated/sdk.gen"
import type { Account, AccountSignInAttempt } from "@/api/generated/types.gen"
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
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { ErrorAlert } from "@/components/error-alert"
import { TextField } from "@/components/text-field"
import { said } from "@/lib/said"

import { useRefreshAccounts } from "./use-models"

export type SignInRuntime = "claude" | "codex" | "antigravity"

type Way = "sign-in" | "token" | "file"

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

// fileSchema checks a credentials file only for its shape, as OmniRoute's import does before sending it
// (ImportClaudeAuthModal.tsx, previewClaudeJson); the server reads it in full.
const fileSchema = z.object({
  label: z.string().trim().max(64, "At most 64 characters."),
  credentials: z
    .string()
    .trim()
    .min(1, "Paste or choose the file.")
    .max(65536, "This is larger than Claude Code's file.")
    .refine((typed) => {
      try {
        const oauth = (JSON.parse(typed) as { claudeAiOauth?: { refreshToken?: unknown } })
          .claudeAiOauth
        return typeof oauth?.refreshToken === "string" && oauth.refreshToken !== ""
      } catch {
        return false
      }
    }, "This is not Claude Code's .credentials.json: it has no claudeAiOauth with a refresh token."),
})

// AccountSignInDialog adds a subscription account on the control plane, or signs one in again (issue #199). It
// has no step before the sign-in: opening it starts one, as 9router's dialog does (OAuthModal.js), so the link
// and the field for what the page gives back are there at once. The account takes its e-mail as its name, as
// 9router names a connection. Claude has a second way beside it, as 9router has "Paste token" beside its browser
// sign-in and OpenClaw has `models auth paste-token`: the token `claude setup-token` prints, which lasts a year.
// A third way imports the session Claude Code already saved in its .credentials.json (issue #828), as OmniRoute's
// "Import Claude auth" does (ImportClaudeAuthModal.tsx): pasted, or read from the chosen file in the browser.
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
  const formId = useId()
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

  const importFile = useMutation({
    mutationFn: async (input: { label: string; credentials: string }) =>
      (
        await importAccount({
          body: {
            runtime: "claude",
            credentials: input.credentials,
            ...(input.label ? { label: input.label } : {}),
          },
        })
      ).data,
    onSuccess: finished,
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

  const file = useForm({
    defaultValues: { label: "", credentials: "" },
    validators: { onSubmit: fileSchema },
    onSubmit: ({ value }) => {
      if (!reading) {
        importFile.mutate({ label: value.label.trim(), credentials: value.credentials.trim() })
      }
    },
  })
  const chooser = useRef<HTMLInputElement>(null)
  // readOf counts the file reads begun: a read writes the field only while it is the latest, so a slow read of an
  // earlier file, or one begun before the dialog closed or the owner typed, never replaces what is there now.
  const readOf = useRef(0)
  const [reading, setReading] = useState(false)

  function forgetReads() {
    readOf.current += 1
    setReading(false)
  }

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
    file.reset()
    forgetReads()
    importFile.reset()
    begin.reset()
    complete.reset()
    addToken.reset()
  }

  const minutes = started ? Math.round(started.expires_in_seconds / 60) : 0
  // A link that could not be made is begun again. After a refused paste the paste can be corrected and sent again:
  // the server keeps a sign-in whose paste it could not read (issue #749), and answers that a used one has ended.
  const spent = begin.isError
  const busy = complete.isPending || addToken.isPending || importFile.isPending || reading

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {account ? `Sign ${account.label} in again` : `Add a ${name} account`}
          </DialogTitle>
          <DialogDescription>
            {way === "file"
              ? "Bring the session Claude Code saved on a machine: its .credentials.json, in ~/.claude or the account's config folder. Hypomnema refreshes it from then on, so that machine may have to sign in again."
              : way === "token"
                ? "On any machine with Claude Code, run claude setup-token and paste the token it prints. It lasts a year; it calls models but may not show the account's limits."
                : `Sign in on ${name}'s own page, in a browser on any machine. Nothing is opened on the server, and your password does not pass through Hypomnema.`}
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
              void (way === "token"
                ? token.handleSubmit()
                : way === "file"
                  ? file.handleSubmit()
                  : finish.handleSubmit())
            }}
          >
            {pastable && (
              <Tabs value={way} onValueChange={(next) => setWay(next as Way)}>
                <TabsList>
                  <TabsTrigger value="sign-in">Sign in</TabsTrigger>
                  <TabsTrigger value="token">Paste a token</TabsTrigger>
                  <TabsTrigger value="file">Import a file</TabsTrigger>
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

            {way === "file" && (
              <FieldGroup>
                <file.Field name="label">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="Its e-mail"
                      description="What you call this account; its e-mail when left empty."
                    />
                  )}
                </file.Field>
                <file.Field name="credentials">
                  {(field) => {
                    const invalid = field.state.meta.isTouched && !field.state.meta.isValid
                    return (
                      <div className="flex flex-col gap-2">
                        <Field data-invalid={invalid || undefined}>
                          <FieldLabel htmlFor={field.name}>.credentials.json</FieldLabel>
                          <input
                            ref={chooser}
                            type="file"
                            accept=".json,application/json"
                            className="hidden"
                            aria-label="Choose the .credentials.json file"
                            onChange={(event) => {
                              const chosen = event.target.files?.[0]
                              event.target.value = ""
                              if (!chosen) return
                              readOf.current += 1
                              const read = readOf.current
                              setReading(true)
                              void chosen
                                .text()
                                .then(
                                  (text) => text,
                                  // A file that cannot be read leaves nothing to send, not the one before it.
                                  () => "",
                                )
                                .then((text) => {
                                  if (read !== readOf.current) return
                                  field.handleChange(text)
                                  field.handleBlur()
                                  setReading(false)
                                })
                            }}
                          />
                          <Textarea
                            id={field.name}
                            name={field.name}
                            rows={5}
                            spellCheck={false}
                            autoComplete="off"
                            placeholder='{ "claudeAiOauth": { … } }'
                            value={field.state.value}
                            aria-invalid={invalid || undefined}
                            onBlur={field.handleBlur}
                            onChange={(event) => {
                              forgetReads()
                              field.handleChange(event.target.value)
                            }}
                          />
                          <FieldDescription>
                            Paste the file, or choose it. It is sealed on the control plane and
                            never shown again.
                          </FieldDescription>
                          {invalid && <FieldError errors={field.state.meta.errors} />}
                        </Field>
                        <div>
                          <Button
                            type="button"
                            variant="outline"
                            onClick={() => chooser.current?.click()}
                          >
                            <FileUp data-icon="inline-start" />
                            Choose file
                          </Button>
                        </div>
                      </div>
                    )
                  }}
                </file.Field>
              </FieldGroup>
            )}

            <ErrorAlert
              error={
                way === "token"
                  ? addToken.error
                  : way === "file"
                    ? importFile.error
                    : (begin.error ?? complete.error)
              }
            />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          {way === "sign-in" && (spent || complete.isError) && (
            <Button type="button" variant={spent ? "default" : "outline"} onClick={startAgain}>
              <RotateCcw data-icon="inline-start" />
              Start again
            </Button>
          )}
          {way === "sign-in" && !spent && started && (
            <Button type="submit" form={formId} disabled={busy}>
              {busy && <Spinner data-icon="inline-start" />}
              Sign in
            </Button>
          )}
          {(way === "token" || way === "file") && (
            <Button type="submit" form={formId} disabled={busy}>
              {busy && <Spinner data-icon="inline-start" />}
              {way === "file" ? "Import account" : "Add account"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
