import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"

import "@/api/client"
import {
  listPushSubscriptionsOptions,
  listPushSubscriptionsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import {
  deletePushSubscription,
  putPushSubscription,
  testPushSubscription,
} from "@/api/generated/sdk.gen"
import type { PushSubscription } from "@/api/generated/types.gen"

import {
  currentSubscription,
  deviceLabel,
  pushSupport,
  subscribe,
  unsubscribeHere,
  type PushSupport,
} from "./push"

// usePush is notifications as the Settings page and the prompt where something waits use them (issue #1047): what
// this browser can do, whether it is one of the owner's devices the control plane pushes to, the list of them, and
// the changes: turn on here, turn off here, remove a device, send one a test, as Coder's
// useWebpushNotifications and its devices list.
export function usePush() {
  const queryClient = useQueryClient()
  const [support] = useState<PushSupport>(() => pushSupport())
  const devices = useQuery(listPushSubscriptionsOptions())
  // This browser's push endpoint: undefined while it is read, null when it has none.
  const [endpoint, setEndpoint] = useState<string | null | undefined>(
    support === "supported" ? undefined : null,
  )
  const [permission, setPermission] = useState(() =>
    support === "supported" ? Notification.permission : "default",
  )

  useEffect(() => {
    if (support !== "supported") return

    let live = true
    currentSubscription()
      .then((s) => live && setEndpoint(s?.endpoint ?? null))
      .catch(() => live && setEndpoint(null))

    return () => {
      live = false
    }
  }, [support])

  const refresh = () => queryClient.invalidateQueries({ queryKey: listPushSubscriptionsQueryKey() })
  // This device is on when the control plane pushes to this browser's endpoint: one it forgot (a 410, new keys)
  // is off, and turning on subscribes again.
  const here = devices.data?.data.find((d) => endpoint && d.endpoint === endpoint)

  const turnOn = useMutation({
    mutationFn: async () => {
      if (!devices.data) throw new Error("The control plane's key is not read yet.")

      try {
        const sub = await subscribe(devices.data.public_key)
        await putPushSubscription({ body: { ...sub, label: deviceLabel(navigator.userAgent) } })
        setEndpoint(sub.endpoint)
      } finally {
        setPermission(Notification.permission)
      }
    },
    onSettled: refresh,
  })

  const remove = useMutation({
    mutationFn: async (device: PushSubscription) => {
      await deletePushSubscription({ path: { subscription_id: device.id } })
      if (device.endpoint === endpoint) {
        await unsubscribeHere()
        setEndpoint(null)
      }
    },
    onSettled: refresh,
  })

  const test = useMutation({
    mutationFn: async (device: PushSubscription) =>
      testPushSubscription({ path: { subscription_id: device.id } }),
    // A device the push service forgot was removed: the list is read again either way.
    onSettled: refresh,
  })

  return { support, permission, devices, endpoint, here, turnOn, remove, test }
}
