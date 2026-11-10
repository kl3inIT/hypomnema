import { createRootRoute, Outlet } from "@tanstack/react-router"
import { useEffect } from "react"

import { LoginScreen } from "@/features/auth/login-screen"
import { checkSession, useSession } from "@/features/auth/session"
import { LiveUpdates } from "@/features/shell/app-shell"

export const Route = createRootRoute({
  component: RootLayout,
})

// The whole window; each screen lays out its own sidebar and header, as t3code's AppSidebarLayout. Without a
// session, the sign-in screen; nothing while the server is asked whether there is one.
function RootLayout() {
  const session = useSession()

  useEffect(() => {
    void checkSession()
  }, [])

  return (
    <div className="flex h-svh bg-background">
      {session === "signed-in" && (
        <>
          <LiveUpdates />
          <Outlet />
        </>
      )}
      {session === "signed-out" && <LoginScreen />}
    </div>
  )
}
