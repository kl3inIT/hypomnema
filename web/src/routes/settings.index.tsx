import { createFileRoute, Navigate } from "@tanstack/react-router"

import { SettingsList } from "@/components/settings-page"
import { useIsMobile } from "@/hooks/use-mobile"

// Settings opens on its first section; a phone, which has no room for the sections beside a page, lists them first,
// as OpenHands' settings index (frontend/src/routes/settings-index.tsx; issue #604).
export const Route = createFileRoute("/settings/")({
  component: function SettingsIndex() {
    const phone = useIsMobile()
    if (!phone) return <Navigate to="/settings/integrations" replace />

    return <SettingsList />
  },
})
