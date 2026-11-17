import { createRootRoute, Outlet, useRouterState } from "@tanstack/react-router"
import { useEffect } from "react"

import { LoginScreen } from "@/features/auth/login-screen"
import { checkSession, useSession } from "@/features/auth/session"
import { SidebarProvider } from "@/components/ui/sidebar"
import { AppDrawer, LiveUpdates } from "@/features/shell/app-shell"
import { rememberAppPage } from "@/lib/back-from-settings"

export const Route = createRootRoute({
  component: RootLayout,
})

// The whole window; each screen lays out its own sidebar and header, as t3code's AppSidebarLayout. Without a
// session, the sign-in screen; nothing while the server is asked whether there is one. shadcn's SidebarProvider
// holds whether a phone's drawer is open, for the header's ≡ and the drawer (issue #604).
function RootLayout() {
  const session = useSession()
  const href = useRouterState({ select: (s) => s.location.href })

  // Settings' "Back to app" returns to the last page outside it (issue #747).
  useEffect(() => rememberAppPage(href), [href])

  useEffect(() => {
    void checkSession()
  }, [])

  return (
    <SidebarProvider>
      <div className="flex h-svh w-full bg-background">
        {session === "signed-in" && (
          <>
            <LiveUpdates />
            <AppDrawer />
            <Outlet />
          </>
        )}
        {session === "signed-out" && <LoginScreen />}
      </div>
    </SidebarProvider>
  )
}
