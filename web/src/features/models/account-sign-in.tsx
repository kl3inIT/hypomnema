import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
import { Copy, ExternalLink } from "lucide-react"
import { useId, useState, type ReactElement } from "react"
import { z } from "zod"

import "@/api/client"
import { beginAccountSignIn, completeAccountSignIn } from "@/api/generated/sdk.gen"
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import { ErrorAlert, TextField } from "@/features/settings/settings-parts"
import { said } from "@/features/settings/use-integrations"

import { useRefreshAccounts } from "./use-models"

export type SignInRuntime = "claude" | "codex" | "antigravity"

const startSchema = z.object({
  label: z.string().trim().max(64, "At most 64 characters."),
})

const finishSchema = z.object({
  callback: z
    .string()
    .trim()
    .refine((typed) => typed.includes("code="), "Paste the whole address the browser ended on."),
})

// AccountSignInDialog signs a subscription account in on the control plane (issue #199, step 2): a new one, or
// one that is there again. It is the Integrations page's sign-in dialog (service-cards.tsx) with the two ways the
// server has: a link whose last address is pasted back, as 9router's dialog asks on a remote host
// (OAuthModal.js), or a link and a code, for which the dialog waits. Nothing is opened on the server, and no
// password passes through Hypomnema.
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
  const [started, setStarted] = useState<AccountSignInAttempt | null>(null)

  const begin = useMutation({
    mutationFn: async (label: string) =>
      (await beginAccountSignIn({ body: { runtime, ...(label ? { label } : {}) } })).data,
  })

  const complete = useMutation({
    mutationFn: async (input: { id: string; callback?: string }) =>
      (
        await completeAccountSignIn({
          path: { sign_in_id: input.id },
          body: input.callback ? { callback: input.callback.trim() } : {},
        })
      ).data,
    onSuccess: async (signedIn) => {
      await refresh()
      change(false)
      said(`${name} account ${signedIn.label} is signed in`)
    },
  })

  const start = useForm({
    defaultValues: { label: "" },
    validators: { onSubmit: startSchema },
    onSubmit: async ({ value }) => {
      let attempt: AccountSignInAttempt

      try {
        attempt = await begin.mutateAsync(value.label.trim())
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      setStarted(attempt)
      // A sign-in with a code has nothing to paste: the server waits for the vendor to say it is done.
      if (!attempt.paste) complete.mutate({ id: attempt.id })
    },
  })

  const finish = useForm({
    defaultValues: { callback: "" },
    validators: { onSubmit: finishSchema },
    onSubmit: ({ value }) => {
      if (started) complete.mutate({ id: started.id, callback: value.callback })
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setStarted(null)
      start.reset()
      finish.reset()
      begin.reset()
      complete.reset()
    }
  }

  const waiting = started !== null && !started.paste
  const minutes = started ? Math.round(started.expires_in_seconds / 60) : 0

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {account ? `Sign ${account.label} in again` : `Add a ${name} account`}
          </DialogTitle>
          <DialogDescription>
            {!started
              ? `You sign in on ${name}'s own page, in a browser on any machine. Nothing is opened on the server, and your password does not pass through Hypomnema.`
              : started.paste
                ? `Sign in on ${name}'s page. The browser then ends on an address that does not load: copy that whole address and paste it here.`
                : `Open ${name}'s page, sign in and enter this code there. This dialog closes by itself when it is done.`}
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
              void (started ? finish.handleSubmit() : start.handleSubmit())
            }}
          >
            {!started && !account && (
              <FieldGroup>
                <start.Field name="label">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="work"
                      description="What you call this account. Its e-mail when left empty."
                    />
                  )}
                </start.Field>
              </FieldGroup>
            )}

            {started && (
              <FieldGroup>
                {/* A link that looks like a button: it leaves this page, so it is an anchor, not a button. */}
                <a
                  href={started.url}
                  target="_blank"
                  rel="noreferrer"
                  className={buttonVariants({ variant: "outline" })}
                >
                  <ExternalLink data-icon="inline-start" />
                  Open {name}&apos;s sign-in page
                </a>
                {started.paste ? (
                  <finish.Field name="callback">
                    {(field) => (
                      <TextField
                        field={field}
                        label="Address the browser ended on"
                        placeholder="http://localhost:54545/callback?code=…&state=…"
                        description={`Within ${minutes} minutes.`}
                      />
                    )}
                  </finish.Field>
                ) : (
                  <Field>
                    <FieldLabel>Code to enter there</FieldLabel>
                    <div className="flex items-center justify-between gap-2 rounded-lg border bg-muted px-3 py-2">
                      <span className="font-mono text-lg tracking-widest" data-testid="user-code">
                        {started.user_code}
                      </span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          void navigator.clipboard.writeText(started.user_code ?? "")
                          said("The code was copied")
                        }}
                      >
                        <Copy data-icon="inline-start" />
                        Copy
                      </Button>
                    </div>
                    <FieldDescription>
                      <span className="inline-flex items-center gap-1.5">
                        {complete.isPending && <Spinner />}
                        {complete.isPending
                          ? `Waiting for you to enter it, for up to ${minutes} minutes.`
                          : "The sign-in did not complete. Close this and start again."}
                      </span>
                    </FieldDescription>
                  </Field>
                )}
              </FieldGroup>
            )}

            <ErrorAlert error={begin.error ?? complete.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          {!waiting && (
            <Button type="submit" form={formId} disabled={begin.isPending || complete.isPending}>
              {(begin.isPending || complete.isPending) && <Spinner data-icon="inline-start" />}
              {started ? "Sign in" : "Continue"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
