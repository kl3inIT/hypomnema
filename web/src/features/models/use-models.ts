import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getAccountLimitsOptions,
  getAccountLimitsQueryKey,
  listAccountsOptions,
  listAccountsQueryKey,
  listGatewayKeysOptions,
  listGatewayKeysQueryKey,
  listGatewayModelsOptions,
  listGatewayModelsQueryKey,
  listProvidersOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import { getAccountLimits } from "@/api/generated/sdk.gen"
import type { AccountLimits } from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

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

// useRefreshGatewayKeys reads the keys again after one is made or revoked: every cached read of their list.
export function useRefreshGatewayKeys() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: everyCall(listGatewayKeysQueryKey()) })
}
