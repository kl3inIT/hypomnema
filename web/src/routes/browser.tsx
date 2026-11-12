import { createFileRoute } from "@tanstack/react-router"

import { BrowserScreen } from "@/features/browser/browser-screen"
import { AppSidebar } from "@/features/shell/app-shell"

export const Route = createFileRoute("/browser")({
  component: () => (
    <>
      <AppSidebar />
      <BrowserScreen />
    </>
  ),
})
