import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"

import "@/api/client"
import {
  getAccountLimitsOptions,
  getAccountLimitsQueryKey,
  getAudioOptions,
  getAudioQueryKey,
  getGatewayUsageOptions,
  getImagesOptions,
  getImagesQueryKey,
  getWebSearchOptions,
  getWebSearchQueryKey,
  listAccountsOptions,
  listAccountsQueryKey,
  listGatewayKeysOptions,
  listGatewayKeysQueryKey,
  listGatewayModelsOptions,
  listGatewayModelsQueryKey,
  listGatewayRequestsOptions,
  listProvidersOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import { getAccountLimits } from "@/api/generated/sdk.gen"
import type { AccountLimits, Audio, Images, WebSearch } from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

import type { Period, RequestPeriod } from "./usage-state"

// An account rests and comes back on its own, so the list is read again now and then while the page is open.
export function useAccounts() {
  return useQuery({
    ...listAccountsOptions(),
    select: (page) => page.data,
    refetchInterval: 30_000,
  })
}

export function useProviders() {
  return useQuery({ ...listProvidersOptions(), select: (page) => page.data })
}

// The models the gateway serves, with their attributes (issue #367). A dialog that lists them reads them only
// while it is open.
export function useGatewayModels({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({ ...listGatewayModelsOptions(), select: (page) => page.data, enabled })
}

// useRefreshGatewayModels reads the gateway's models again after one changes: every cached read of their list.
export function useRefreshGatewayModels() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listGatewayModelsQueryKey()) })
}

export function useRefreshAccounts() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listAccountsQueryKey()) })
}

// An account's limits. The server keeps a reading for a minute, so the page asks again each minute while open.
export function useAccountLimits(accountId: string, enabled: boolean) {
  return useQuery({
    ...getAccountLimitsOptions({ path: { account_id: accountId } }),
    enabled,
    refetchInterval: 60_000,
  })
}

// useShowLimits shows limits the server answered with, such as after a reset, as an account's limits: the body
// of their read, which the limits themselves are.
export function useShowLimits(accountId: string) {
  const queryClient = useQueryClient()

  return (limits: AccountLimits) =>
    queryClient.setQueryData(getAccountLimitsQueryKey({ path: { account_id: accountId } }), limits)
}

// useRefreshLimits asks the vendor for an account's limits now and shows what it answers.
export function useRefreshLimits(accountId: string) {
  const show = useShowLimits(accountId)

  return useMutation({
    mutationFn: async () =>
      (await getAccountLimits({ path: { account_id: accountId }, query: { fresh: true } })).data,
    onSuccess: show,
  })
}

// The gateway's keys for tools outside, with the count of the keys made for workers and Bots.
export function useGatewayKeys() {
  return useQuery(listGatewayKeysOptions())
}

// useRefreshGatewayKeys reads the keys again after a change, invalidating every cached read of the list.
export function useRefreshGatewayKeys() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listGatewayKeysQueryKey()) })
}

// The image providers in their order. A rest ends on its own, so they are read again now and then while the page
// is open.
export function useImages() {
  return useQuery({ ...getImagesOptions(), refetchInterval: 30_000 })
}

// useShowImages shows the providers the server answered a change with: the body of their read.
export function useShowImages() {
  const queryClient = useQueryClient()

  return (images: Images) => queryClient.setQueryData(getImagesQueryKey(), images)
}

// The audio providers and each kind's order. A key's rest ends on its own, so they are read again now and then while
// the page is open.
export function useAudio() {
  return useQuery({ ...getAudioOptions(), refetchInterval: 30_000 })
}

// useShowAudio shows the providers the server answered a change with: the body of their read.
export function useShowAudio() {
  const queryClient = useQueryClient()

  return (audio: Audio) => queryClient.setQueryData(getAudioQueryKey(), audio)
}

// The web search chain. A rest ends on its own, so the chain is read again now and then while the page is open.
export function useWebSearch() {
  return useQuery({ ...getWebSearchOptions(), refetchInterval: 30_000 })
}

// useShowWebSearch shows the chain the server answered a change with: the body of its read.
export function useShowWebSearch() {
  const queryClient = useQueryClient()

  return (web: WebSearch) => queryClient.setQueryData(getWebSearchQueryKey(), web)
}

// What the gateway served in a period, counted by the viewer's own day: the offset is part of the generated key,
// so it is taken once, when the page opens. The last period's answer stays shown while the next one is read.
export function useGatewayUsage(period: Period) {
  const [utcOffset] = useState(() => -new Date().getTimezoneOffset())

  return useQuery({
    ...getGatewayUsageOptions({ query: { period, utc_offset_minutes: utcOffset } }),
    placeholderData: keepPreviousData,
  })
}

// A page of the requests the gateway served in a period, all or only the failed ones. The last page stays shown
// while the next one is read.
export function useGatewayRequests(
  period: RequestPeriod,
  failedOnly: boolean,
  offset: number,
  limit: number,
) {
  return useQuery({
    ...listGatewayRequestsOptions({
      query: { period, failed: failedOnly || undefined, offset, limit },
    }),
    placeholderData: keepPreviousData,
  })
}
