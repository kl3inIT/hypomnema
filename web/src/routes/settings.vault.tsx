import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/components/settings-page"
import { VaultScreen } from "@/features/settings/vault-screen"

export const Route = createFileRoute("/settings/vault")({
  component: () => (
    <>
      <SettingsSidebar />
      <VaultScreen />
    </>
  ),
})
