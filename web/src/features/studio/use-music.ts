import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  deleteVideoMusicMutation,
  listVideoLooksQueryKey,
  listVideoMusicOptions,
  listVideoMusicQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"

import { uploadTrack } from "./music-state"

// The reads and writes of the Music group (issue #910, rows 6a, 6d and 18): the library with its quota, a track's
// upload and its delete.

// useVideoMusic is GET /videos/music, the library, while the Look is shown.
export const useVideoMusic = (enabled: boolean) => useQuery({ ...listVideoMusicOptions(), enabled })

// useUploadTrack uploads a file as a track; the library is read again after it.
export function useUploadTrack() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: (file: File) => uploadTrack(file),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: listVideoMusicQueryKey() }),
  })
}

// useDeleteTrack deletes a track; the library is read again, and the looks too, since the saved default's music
// becomes none when it named the track (row 6d).
export function useDeleteTrack() {
  const queryClient = useQueryClient()

  return useMutation({
    ...deleteVideoMusicMutation(),
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: listVideoMusicQueryKey() }),
        queryClient.invalidateQueries({ queryKey: listVideoLooksQueryKey() }),
      ]),
  })
}
