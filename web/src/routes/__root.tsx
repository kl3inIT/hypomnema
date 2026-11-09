import { createRootRoute, Outlet } from "@tanstack/react-router"

export const Route = createRootRoute({
  component: RootLayout,
})

// Phone first: one column with a header. The sidebar for wide screens comes with the second screen.
function RootLayout() {
  return (
    <div className="flex min-h-svh flex-col">
      <header className="flex h-12 shrink-0 items-center border-b px-4">
        <span className="font-medium">Hypomnema</span>
      </header>
      <main className="flex min-h-0 flex-1 flex-col">
        <Outlet />
      </main>
    </div>
  )
}
