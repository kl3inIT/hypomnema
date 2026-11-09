import { createRootRoute, Outlet } from "@tanstack/react-router"

import { LoginScreen } from "@/features/auth/login-screen"
import { useToken } from "@/features/auth/token"
import { LiveUpdates } from "@/features/shell/app-shell"

export const Route = createRootRoute({
  component: RootLayout,
})

// The whole window; each screen lays out its own sidebar and header, as t3code's AppSidebarLayout. Without a token,
// the sign-in screen.
function RootLayout() {
  const token = useToken()

  return (
    <div className="flex h-svh bg-background">
      {token ? (
        <>
          <LiveUpdates />
          <Outlet />
        </>
      ) : (
        <LoginScreen />
      )}
    </div>
  )
}
