import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getAccountLimits,
  listAccounts,
  listGatewayModels,
  listProviders,
} from "@/api/generated/sdk.gen"

export const accountsKey = ["accounts"] as const
export const providersKey = ["model-providers"] as const
export const gatewayModelsKey = ["gateway-models"] as const

// An account rests and comes back on its own, so the list is read again now and then while the page is open.
export function useAccounts() {
  return useQuery({
    queryKey: accountsKey,
    queryFn: async () => (await listAccounts()).data.data,
    refetchInterval: 30_000,
  })
}

export function useProviders() {
  return useQuery({
    queryKey: providersKey,
    queryFn: async () => (await listProviders()).data.data,
  })
}

export function useGatewayModels() {
  return useQuery({
    queryKey: gatewayModelsKey,
    queryFn: async () => (await listGatewayModels()).data.data,
  })
}

export function useRefreshAccounts() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: accountsKey })
}

export const limitsKey = (accountId: string) => ["account-limits", accountId] as const

// An account's limits. The server keeps a reading for a minute, so the page asks again each minute while open.
export function useAccountLimits(accountId: string, enabled: boolean) {
  return useQuery({
    queryKey: limitsKey(accountId),
    queryFn: async () => (await getAccountLimits({ path: { account_id: accountId } })).data,
    enabled,
    refetchInterval: 60_000,
  })
}

// useRefreshLimits asks the vendor for an account's limits now and shows what it answers.
export function useRefreshLimits(accountId: string) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async () =>
      (await getAccountLimits({ path: { account_id: accountId }, query: { fresh: true } })).data,
    onSuccess: (limits) => queryClient.setQueryData(limitsKey(accountId), limits),
  })
}
