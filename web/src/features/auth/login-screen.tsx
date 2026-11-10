import { useState } from "react"

import { getMe } from "@/api/generated/sdk.gen"
import { Button } from "@/components/ui/button"

import { saveToken } from "./token"

// The first visit: the owner pastes a token made with `hypomnema tokens add <name> --kind owner`. It is checked
// against the server before it is kept.
export function LoginScreen() {
  const [token, setToken] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setChecking(true)
    setError(null)

    try {
      const { data: me } = await getMe({ auth: token.trim() })
      if (me.kind !== "owner") {
        setError("This is a coordinator token. Chats need an owner token.")
      } else {
        saveToken(token.trim())
      }
    } catch {
      setError("The server did not accept this token.")
    } finally {
      setChecking(false)
    }
  }

  return (
    <div className="mx-auto flex w-full max-w-sm flex-1 flex-col justify-center gap-4 p-6">
      <h1 className="text-lg font-medium">Sign in</h1>
      <p className="text-sm text-muted-foreground">
        Paste an owner token. Make one on the server with{" "}
        <code className="font-mono">hypomnema tokens add web --kind owner</code>.
      </p>
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <input
          type="password"
          aria-label="Owner token"
          autoComplete="off"
          value={token}
          onChange={(event) => setToken(event.target.value)}
          className="h-10 rounded-md border border-input bg-background px-3 font-mono text-sm"
        />
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <Button type="submit" disabled={checking || token.trim() === ""}>
          {checking ? "Checking…" : "Sign in"}
        </Button>
      </form>
    </div>
  )
}
