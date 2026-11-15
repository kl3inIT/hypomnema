import { useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  getSecretValueOptions,
  getSecretValueQueryKey,
  listSecretsOptions,
  listSecretsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { everyCall } from "@/lib/query-keys"

// useSecrets reads the secrets in the vault, without their values.
export function useSecrets() {
  return useQuery({ ...listSecretsOptions(), select: (page) => page.data })
}

// useSecretValue reads a secret's values only while they are shown, and does not keep them once hidden.
export function useSecretValue(name: string, shown: boolean) {
  return useQuery({
    ...getSecretValueOptions({ path: { name } }),
    select: (secret) => secret.values,
    enabled: shown,
    gcTime: 0,
    staleTime: 0,
  })
}

// useRefreshSecrets reads the secrets again after one changes, then the values still shown, which may be the old
// ones. The list comes first, so a deleted secret's row is gone before the values are read again; what waits for
// the refresh waits for the list only.
export function useRefreshSecrets() {
  const queryClient = useQueryClient()

  return async () => {
    await queryClient.invalidateQueries({ queryKey: everyCall(listSecretsQueryKey()) })
    void queryClient.invalidateQueries({
      queryKey: everyCall(getSecretValueQueryKey({ path: { name: "" } })),
    })
  }
}
