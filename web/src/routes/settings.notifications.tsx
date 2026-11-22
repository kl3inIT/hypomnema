import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { NotificationsScreen } from "@/features/notifications/notifications-screen"

export const Route = createFileRoute("/settings/notifications")({
  component: () => (
    <>
      <SettingsSidebar />
      <NotificationsScreen />
    </>
  ),
})
