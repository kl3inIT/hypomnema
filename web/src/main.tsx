import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider } from "@tanstack/react-router"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import "./index.css"
import { ThemeProvider } from "@/components/theme-provider"
import { Toaster } from "@/components/ui/toast"
import { TooltipProvider } from "@/components/ui/tooltip"
import { registerServiceWorker } from "@/features/notifications/push"
import { router } from "@/router"

// What was read stays fresh for 30 seconds (issue #510), as Omnara's and MemoryOS's clients keep it: a screen opened
// again shows it without asking the server for the lists every screen shares. Live events and each change still
// mark what they touch as stale, and a query that sets its own staleTime or refetchInterval keeps it.
const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: 30_000 } } })

const root = document.getElementById("root")
if (!root) {
  throw new Error("the #root element is missing")
}

// The service worker shows Web Push notifications (issue #1047); it handles no fetch and caches nothing.
registerServiceWorker()

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider defaultTheme="dark">
        {/* One provider for every tooltip, and the place toasts show, for any screen that needs them. */}
        <TooltipProvider delay={300}>
          <RouterProvider router={router} />
          <Toaster />
        </TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
