import { createRootRoute, Link, Outlet } from "@tanstack/react-router"
import { LogOut } from "lucide-react"

import { Button } from "@/components/ui/button"
import { LoginScreen } from "@/features/auth/login-screen"
import { saveToken, useToken } from "@/features/auth/token"

export const Route = createRootRoute({
  component: RootLayout,
})

// One column on a phone; the chat list joins as a column on wide screens. Without a token, the sign-in screen.
function RootLayout() {
  const token = useToken()

  return (
    <div className="flex h-svh flex-col">
      <header className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
        <Link to="/" className="font-medium">
          Hypomnema
        </Link>
        {token && (
          <Button variant="ghost" size="sm" className="ml-auto" onClick={() => saveToken(null)}>
            <LogOut />
            Sign out
          </Button>
        )}
      </header>
      <main className="flex min-h-0 flex-1 flex-col">{token ? <Outlet /> : <LoginScreen />}</main>
    </div>
  )
}
