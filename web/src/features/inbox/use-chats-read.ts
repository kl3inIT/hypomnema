import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  listInboxChatSourcesOptions,
  listInboxChatSourcesQueryKey,
  listInboxChatsOptions,
  listInboxChatsQueryKey,
  setInboxChatMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import type { InboxChatKind, InboxChatState } from "@/api/generated/types.gen"
import { callsOf } from "@/lib/query-keys"

// useChatSources reads the owner's accounts whose chats the secretary may read, with their counts.
export function useChatSources() {
  return useQuery(listInboxChatSourcesOptions())
}

// ChatFilter is what the list is narrowed to.
export type ChatFilter = { q: string; kind?: InboxChatKind; state?: InboxChatState }

// useChats reads an account's chats, searched and filtered by the server.
export function useChats(integration: string, label: string, filter: ChatFilter) {
  return useQuery({
    ...listInboxChatsOptions({
      path: { integration, label },
      query: {
        ...(filter.q ? { q: filter.q } : {}),
        ...(filter.kind ? { kind: filter.kind } : {}),
        ...(filter.state ? { state: filter.state } : {}),
      },
    }),
    enabled: Boolean(integration && label),
    placeholderData: keepPreviousData,
  })
}

// useSetChat switches whether the secretary reads a chat, then reads the lists and their counts again.
export function useSetChat() {
  const queryClient = useQueryClient()

  return useMutation({
    ...setInboxChatMutation(),
    onSettled: () =>
      queryClient.invalidateQueries({
        predicate: callsOf(
          listInboxChatsQueryKey({ path: { integration: "", label: "" } }),
          listInboxChatSourcesQueryKey(),
        ),
      }),
  })
}
