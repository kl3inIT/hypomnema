import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useCallback } from "react"

import "@/api/client"
import {
  getBotOptions,
  getBotQueryKey,
  listBotsOptions,
  listBotsQueryKey,
  listBotVersionsOptions,
  listBotVersionsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import {
  createBot,
  deleteBot,
  putBot,
  renameBot,
  restoreBotVersion,
  reorderBots as putBotOrder,
  setBotMemory,
  setBotRepos,
} from "@/api/generated/sdk.gen"
import type { Bot, BotList, BotSettings } from "@/api/generated/types.gen"
import { callsOf } from "@/lib/query-keys"

import { reorderBots } from "@/features/shell/bot-rows"

import { botNameOf, mainBotOf } from "./bots-state"

// whileOpen is how often the Bots are read again while a page shows them: a Bot writes its own memory in a
// chat, without an event.
const whileOpen = 10_000

// useBots reads every Bot, with its instructions and memory.
export function useBots() {
  return useQuery({
    ...listBotsOptions(),
    select: (page) => page.data,
    refetchInterval: whileOpen,
  })
}

// useMainBot is the id of the main Bot, the one a chat talks with when none is named; "" until the Bots are read.
export function useMainBot() {
  return mainBotOf(useBots().data)
}

// useBotNames names Bots by their ids: each by its name, read with the Bots, since it may change (issue #466); a Bot
// removed since by the name it had or as "A removed Bot", never by its id (issue #1139); its id until they are read.
export function useBotNames() {
  const list = useQuery({ ...listBotsOptions(), refetchInterval: whileOpen }).data

  return useCallback((id: string) => botNameOf(list, id), [list])
}

// useBotName is what the Bot of an id is called, as useBotNames names it.
export function useBotName(id: string) {
  return useBotNames()(id)
}

// useBot reads one Bot by its id.
export function useBot(id: string) {
  return useQuery({
    ...getBotOptions({ path: { bot_id: id } }),
    refetchInterval: whileOpen,
    retry: false,
  })
}

// useRefreshBots reads the Bots again after one changes: every cached read of their list and of one Bot, which the
// hand-written ["bots"] prefix covered.
export function useRefreshBots() {
  const queryClient = useQueryClient()

  return () =>
    queryClient.invalidateQueries({
      predicate: callsOf(listBotsQueryKey(), getBotQueryKey({ path: { bot_id: "" } })),
    })
}

// saved puts a Bot the server answered in the cache, under its generated key, and reads the Bots again.
function useSaved() {
  const queryClient = useQueryClient()
  const refresh = useRefreshBots()

  return (bot: Bot) => {
    queryClient.setQueryData(getBotQueryKey({ path: { bot_id: bot.id } }), bot)
    // A save that changed the settings kept a version (issue #1336).
    void queryClient.invalidateQueries({
      queryKey: listBotVersionsQueryKey({ path: { bot_id: bot.id } }),
    })

    return refresh()
  }
}

// usePutBot makes a Bot, when it has no id, or replaces its model, effort and instructions; its name and memory
// stay.
// putBotKey marks every save of a Bot, so that one part of its page waits while another part saves: each PUT
// carries the whole of the Bot's settings.
export const putBotKey = ["put-bot"] as const

export function usePutBot() {
  const saved = useSaved()

  return useMutation({
    mutationKey: putBotKey,
    mutationFn: async ({
      id,
      name,
      settings,
    }: {
      id?: string
      name?: string
      settings: BotSettings
    }) =>
      id
        ? (await putBot({ path: { bot_id: id }, body: settings })).data
        : (await createBot({ body: { name: name ?? "", settings } })).data,
    onSuccess: saved,
  })
}

// useRenameBot gives a Bot another name; its id, chats, Routines and Channels stay (issue #466).
export function useRenameBot() {
  const saved = useSaved()

  return useMutation({
    mutationKey: putBotKey,
    mutationFn: async ({ id, name }: { id: string; name: string }) =>
      (await renameBot({ path: { bot_id: id }, body: { name } })).data,
    onSuccess: saved,
  })
}

// useBotsChange changes the Bots in the cache at once and then on the server, as onyx shows a new pin or order of its
// agents before the server answers and puts back what it had when the server refuses (web/src/lib/agents/hooks.ts,
// usePinnedAgents); the Bots are read again either way.
function useBotsChange<T>(
  change: (bots: Bot[], to: T) => Bot[],
  send: (to: T) => Promise<unknown>,
) {
  const queryClient = useQueryClient()
  const refresh = useRefreshBots()
  const listKey = listBotsQueryKey()

  return useMutation({
    mutationFn: send,
    onMutate: async (to: T) => {
      await queryClient.cancelQueries({ queryKey: listKey, exact: true })
      const had = queryClient.getQueryData<BotList>(listKey)
      if (had) queryClient.setQueryData(listKey, { ...had, data: change(had.data, to) })

      return { had }
    },
    onError: (_error, _to, context) => {
      if (context?.had) queryClient.setQueryData(listKey, context.had)
    },
    onSettled: refresh,
  })
}

// useReorderBots moves the Bots named in the one order every client lists them in (issue #470): they take the
// places they hold together, the others keep theirs.
export function useReorderBots() {
  return useBotsChange(reorderBots, async (ids: string[]) => {
    await putBotOrder({ body: { bot_ids: ids } })
  })
}

// useMoveBot puts a Bot in a department, or in none with "" (issue #471), shown in the sidebar before the server
// answers.
export function useMoveBot() {
  return useBotsChange(
    (bots, { id, department }: { id: string; department: string }) =>
      bots.map((bot) => (bot.id === id ? { ...bot, department: department || undefined } : bot)),
    async ({ id, department }) => {
      await renameBot({ path: { bot_id: id }, body: { department } })
    },
  )
}

// useSetMainChat makes one of a Bot's chats its main chat (issue #923), as OpenMausBot's "Set as main thread"
// patches the bot's mainThreadChoice (src/components/SidebarBotMenu.tsx; Apache License 2.0): the Bot points at it at
// once, and back when the server refuses.
export function useSetMainChat() {
  return useBotsChange(
    (bots, { id, chat }: { id: string; chat: string }) =>
      bots.map((bot) => (bot.id === id ? { ...bot, main_chat: chat } : bot)),
    async ({ id, chat }) => {
      await renameBot({ path: { bot_id: id }, body: { main_chat: chat } })
    },
  )
}

// useSetBotMemory replaces a Bot's whole memory with the owner's text, a line an entry. The server answers with the
// memory as kept, in the shape a profile's has (issue #1336), which goes into the cached Bot.
export function useSetBotMemory() {
  const queryClient = useQueryClient()
  const refresh = useRefreshBots()

  return useMutation({
    mutationFn: async ({ id, memory }: { id: string; memory: string }) =>
      (await setBotMemory({ path: { bot_id: id }, body: { memory } })).data,
    onSuccess: (kept, { id }) => {
      queryClient.setQueryData(getBotQueryKey({ path: { bot_id: id } }), (bot: Bot | undefined) =>
        bot ? { ...bot, memory: kept.memory } : bot,
      )

      return refresh()
    },
  })
}

// useSetBotRepos replaces the repositories whose work a Bot owns (issue #1334): a Run created on one goes to its chat.
// The server refuses a repository another Bot owns, naming that Bot.
export function useSetBotRepos() {
  const saved = useSaved()

  return useMutation({
    mutationFn: async ({ id, repos }: { id: string; repos: string[] }) =>
      (await setBotRepos({ path: { bot_id: id }, body: { repos } })).data,
    onSuccess: saved,
  })
}

// useDeleteBot removes a Bot; the server refuses the main one, one with chats and one with a memory.
export function useDeleteBot() {
  return useMutation({
    // force removes the Bot with its chats and its memory (issue #330); only the owner may.
    mutationFn: async ({ id, force = false }: { id: string; force?: boolean }) => {
      await deleteBot({ path: { bot_id: id }, query: force ? { force } : undefined })
    },
  })
}

// useBotVersions reads a Bot's saved settings, newest first (issue #1336).
export function useBotVersions(id: string) {
  return useQuery({
    ...listBotVersionsOptions({ path: { bot_id: id } }),
    select: (page) => page.data,
  })
}

// useRestoreBotVersion saves one of a Bot's versions again, as a new version restored from it; the server answers
// with the Bot, which goes in the cache as a save's does.
export function useRestoreBotVersion() {
  const saved = useSaved()

  return useMutation({
    mutationFn: async ({ id, versionId }: { id: string; versionId: string }) =>
      (await restoreBotVersion({ path: { bot_id: id, version_id: versionId } })).data,
    onSuccess: saved,
  })
}
