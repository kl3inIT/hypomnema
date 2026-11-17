import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  fixMeetingWordMutation,
  getMeetingOptions,
  getMeetingQueryKey,
  getMeetingSettingsOptions,
  getMeetingSettingsQueryKey,
  listMeetingActionsOptions,
  listMeetingActionsQueryKey,
  listMeetingWordsOptions,
  listMeetingWordsQueryKey,
  listMeetingsOptions,
  listMeetingsQueryKey,
  nameMeetingSpeakerMutation,
  setMeetingLineSpeakerMutation,
  setMeetingSettingsMutation,
  setMeetingWordsMutation,
  updateMeetingItemMutation,
  uploadMeetingMutation,
  writeMeetingNotesMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import type { MeetingDetail } from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

import { working } from "./meetings-state"

// whileWorking is how often a meeting the server is still hearing or writing up is read again: its steps change
// without an event.
const whileWorking = 3000

// useMeetings reads the meetings, those whose title or transcript holds q; while one is being worked on, again
// every few seconds.
export function useMeetings(q: string) {
  return useQuery({
    ...listMeetingsOptions({ query: q ? { q } : {} }),
    select: (page) => page.data,
    placeholderData: keepPreviousData,
    refetchInterval: (query) =>
      query.state.data?.data.some((m) => working(m)) ? whileWorking : false,
  })
}

// useMeeting reads one meeting with its transcript and notes, again every few seconds while it is worked on.
export function useMeeting(id: string) {
  return useQuery({
    ...getMeetingOptions({ path: { meeting_id: id } }),
    refetchInterval: (query) => (working(query.state.data?.meeting) ? whileWorking : false),
  })
}

// useRefreshMeetings reads every list and page of meetings again after one changes.
export function useRefreshMeetings() {
  const queryClient = useQueryClient()

  return () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: everyCall(listMeetingsQueryKey()) }),
      queryClient.invalidateQueries({ queryKey: everyCall(listMeetingActionsQueryKey()) }),
    ])
}

// usePutMeeting keeps the meeting an answer gave, so the page shows it at once, and reads the lists again.
function usePutMeeting(id: string) {
  const queryClient = useQueryClient()
  const refresh = useRefreshMeetings()

  return async (detail?: MeetingDetail) => {
    if (detail) queryClient.setQueryData(getMeetingQueryKey({ path: { meeting_id: id } }), detail)
    else
      await queryClient.invalidateQueries({
        queryKey: getMeetingQueryKey({ path: { meeting_id: id } }),
      })
    await refresh()
  }
}

export function useUploadMeeting() {
  const refresh = useRefreshMeetings()

  return useMutation({ ...uploadMeetingMutation(), onSuccess: () => refresh() })
}

export function useWriteNotes(id: string) {
  const put = usePutMeeting(id)

  return useMutation({ ...writeMeetingNotesMutation(), onSuccess: () => put() })
}

export function useNameSpeaker(id: string) {
  const put = usePutMeeting(id)

  return useMutation({ ...nameMeetingSpeakerMutation(), onSuccess: (detail) => put(detail) })
}

export function useLineSpeaker(id: string) {
  const put = usePutMeeting(id)

  return useMutation({ ...setMeetingLineSpeakerMutation(), onSuccess: (detail) => put(detail) })
}

// useMarkDone marks an action done or not, from a meeting's page or the Actions page.
export function useMarkDone() {
  const queryClient = useQueryClient()
  const refresh = useRefreshMeetings()

  return useMutation({
    ...updateMeetingItemMutation(),
    onSuccess: (_, vars) =>
      Promise.all([
        queryClient.invalidateQueries({
          queryKey: getMeetingQueryKey({ path: { meeting_id: vars.path.meeting_id } }),
        }),
        refresh(),
      ]),
  })
}

// useMeetingActions reads the actions of every meeting, of one person and in one state.
export function useMeetingActions(who: string, state: "open" | "done" | "all") {
  return useQuery({
    ...listMeetingActionsOptions({ query: { ...(who ? { who } : {}), state } }),
    placeholderData: keepPreviousData,
  })
}

export function useMeetingSettings() {
  return useQuery(getMeetingSettingsOptions())
}

export function useSetMeetingSettings() {
  const queryClient = useQueryClient()

  return useMutation({
    ...setMeetingSettingsMutation(),
    onSuccess: (settings) => queryClient.setQueryData(getMeetingSettingsQueryKey(), settings),
  })
}

// useFixWord replaces a word heard wrong in a line or the whole meeting (issue #747); the right word joins the word
// list, which is read again.
export function useFixWord(id: string) {
  const queryClient = useQueryClient()
  const put = usePutMeeting(id)

  return useMutation({
    ...fixMeetingWordMutation(),
    onSuccess: (fixed) =>
      Promise.all([
        put(fixed.detail),
        queryClient.invalidateQueries({ queryKey: listMeetingWordsQueryKey() }),
      ]),
  })
}

// useMeetingWords reads the words to hear right: the owner's and the names found.
export function useMeetingWords() {
  return useQuery(listMeetingWordsOptions())
}

export function useSetMeetingWords() {
  const queryClient = useQueryClient()

  return useMutation({
    ...setMeetingWordsMutation(),
    onSuccess: (words) => queryClient.setQueryData(listMeetingWordsQueryKey(), words),
  })
}

// recordingUrl is where the player reads a meeting's recording; the session's cookie signs the request in.
export function recordingUrl(id: string) {
  return `/api/v1/meetings/${encodeURIComponent(id)}/recording`
}
