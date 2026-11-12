import { useState } from "react"

import { HypomnemaIcon, HypomnemaWordmark } from "@/components/icons/brand-icons"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

import { signIn, SignInError } from "./session"

// The first visit, and any visit after the session ended: a user made on the server with
// `hypomnema users add <name>` signs in with the name and the password. The page is Coder's (LoginPageView.tsx,
// SignInForm.tsx): the product's mark over a large heading, then the form, here in a card.
export function LoginScreen() {
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setChecking(true)
    setError(null)

    try {
      await signIn(username.trim(), password)
    } catch (refused) {
      setError(
        refused instanceof SignInError ? refused.message : "The server could not be reached.",
      )
    } finally {
      setChecking(false)
    }
  }

  return (
    <div className="mx-auto flex w-full max-w-sm flex-1 flex-col justify-center gap-6 p-6">
      <div className="flex items-center justify-center gap-2.5">
        <HypomnemaIcon className="size-10 shrink-0" />
        <HypomnemaWordmark className="h-7 w-auto" />
        <span className="sr-only">Hypomnema</span>
      </div>
      <h1 className="text-center text-2xl font-semibold tracking-tight">Sign in</h1>
      <Card>
        <CardContent>
          <form className="flex flex-col gap-4" onSubmit={submit}>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="username">Username</FieldLabel>
                <Input
                  id="username"
                  type="text"
                  name="username"
                  autoComplete="username"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={username}
                  onChange={(event) => setUsername(event.target.value)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="password">Password</FieldLabel>
                <Input
                  id="password"
                  type="password"
                  name="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                />
              </Field>
            </FieldGroup>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <Button type="submit" disabled={checking || username.trim() === "" || password === ""}>
              {checking && <Spinner data-icon="inline-start" />}
              {checking ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
