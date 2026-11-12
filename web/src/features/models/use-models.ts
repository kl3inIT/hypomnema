import { useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { listAccounts, listGatewayModels, listProviders } from "@/api/generated/sdk.gen"

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
