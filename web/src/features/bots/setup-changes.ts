import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { listSetupChanges, undoSetupChange } from "@/api/generated/sdk.gen"
import type { Bot, SetupChange } from "@/api/generated/types.gen"
import { profilesKey } from "@/features/agents/use-profiles"
import { said } from "@/lib/said"

import { botNameOf } from "./bots-state"
import { botsKey } from "./use-bots"

// The setup changes of a chat (issue #169): what the main Bot made, changed or removed of Bots and
// profiles there, read and undone. Their card is setup-change-card.tsx.

const setupChangesKey = (chatId: string) => ["setup-changes", chatId] as const

// useSetupChanges reads the changes made from a chat; read again while the chat is open, as the Bot makes them in
// its turn without an event.
export function useSetupChanges(chatId: string) {
  return useQuery({
    queryKey: setupChangesKey(chatId),
    queryFn: async () => (await listSetupChanges({ query: { chat_id: chatId } })).data.data,
    refetchInterval: 10_000,
  })
}

export function useUndoSetupChange() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (change: SetupChange) =>
      (await undoSetupChange({ path: { change_id: change.id } })).data,
    onSuccess: async (change) => {
      said(
        `${verb.undone[change.action]} ${what(change, botNameOf(queryClient.getQueryData<Bot[]>(botsKey), change.name))}`,
      )
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: setupChangesKey(change.chat_id) }),
        queryClient.invalidateQueries({ queryKey: botsKey }),
        queryClient.invalidateQueries({ queryKey: profilesKey }),
      ])
    },
  })
}

export const verb = {
  done: { made: "Made", changed: "Changed", removed: "Removed" },
  undone: { made: "Removed", changed: "Put back", removed: "Put back" },
} as const

// what names the thing a change was about: "Bot researcher", "profile checker".
// what names the thing changed. A Bot's change names it by its id (issue #466): botName is its name, when it is
// still there to be read.
export function what(change: Pick<SetupChange, "kind" | "name">, botName?: string) {
  return change.kind === "bot" ? `Bot ${botName ?? change.name}` : `profile ${change.name}`
}

// changeLine says where a change stands: undone, can be undone, or why not.
export function changeLine(change: SetupChange) {
  if (change.undone_at) return "Undone."
  if (change.can_undo) return "Applied. You can undo it."

  return change.problem ? `Can't be undone: ${change.problem}.` : "Can't be undone."
}
