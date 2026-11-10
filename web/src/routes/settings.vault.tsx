import { createFileRoute } from "@tanstack/react-router"

import { SettingsSidebar } from "@/features/settings/settings-shell"
import { VaultScreen } from "@/features/settings/vault-screen"

export const Route = createFileRoute("/settings/vault")({
  component: () => (
    <>
      <SettingsSidebar />
      <VaultScreen />
    </>
  ),
})
