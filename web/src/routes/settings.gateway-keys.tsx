import { createFileRoute } from "@tanstack/react-router"

import { GatewayKeysScreen } from "@/features/models/gateway-keys-screen"
import { SettingsSidebar } from "@/features/settings/settings-shell"

export const Route = createFileRoute("/settings/gateway-keys")({
  component: () => (
    <>
      <SettingsSidebar />
      <GatewayKeysScreen />
    </>
  ),
})
