// What the web client knows of the Android phone a node holds (issue #417): GET /device, read again while a page
// shows it, as the browser's tabs are (../browser/browser-state.ts).
import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { getDevice } from "@/api/generated/sdk.gen"

const deviceKey = ["device"] as const

const whileOpen = 5000

export function useDevice() {
  return useQuery({
    queryKey: deviceKey,
    queryFn: async () => (await getDevice()).data,
    // A control plane with no phone is not asked again and again.
    refetchInterval: (query) => (query.state.status === "error" ? false : whileOpen),
    retry: false,
  })
}

// hasPhone says whether a node of the control plane holds a phone, which a pane can then open.
export const hasPhone = (data: { data: unknown[] } | undefined) => (data?.data.length ?? 0) > 0
