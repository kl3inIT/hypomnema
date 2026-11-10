import { useState } from "react"

import { Button } from "@/components/ui/button"

import { signIn, SignInError } from "./session"

const inputClass = "h-10 rounded-md border border-input bg-background px-3 text-sm"

// The first visit, and any visit after the session ended: a user made on the server with
// `hypomnema users add <name>` signs in with the name and the password.
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
    <div className="mx-auto flex w-full max-w-sm flex-1 flex-col justify-center gap-4 p-6">
      <h1 className="text-lg font-medium">Sign in</h1>
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <label className="flex flex-col gap-1.5 text-sm">
          Username
          <input
            type="text"
            name="username"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            className={inputClass}
          />
        </label>
        <label className="flex flex-col gap-1.5 text-sm">
          Password
          <input
            type="password"
            name="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            className={inputClass}
          />
        </label>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <Button type="submit" disabled={checking || username.trim() === "" || password === ""}>
          {checking ? "Signing in…" : "Sign in"}
        </Button>
      </form>
    </div>
  )
}
