import { keepPreviousData, useQuery } from "@tanstack/react-query"

import "@/api/client"
import {
  listVideoLooksOptions,
  listVideoTemplatesOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import { previewVideoLook } from "@/api/generated/sdk.gen"
import type { VideoPreview } from "@/api/generated/types.gen"
import { useObjectURL } from "@/hooks/use-object-url"

// The reads of the Look section (issue #908, rows 14-16; #938): the catalogue with the saved default, and the look
// sample of what is chosen.

// useVideoLooks is GET /videos/looks while the section is shown.
export const useVideoLooks = (enabled: boolean) => useQuery({ ...listVideoLooksOptions(), enabled })

// useVideoTemplates is GET /videos/templates while Make video is open (issue #1230, row 5).
export const useVideoTemplates = (enabled: boolean) =>
  useQuery({ ...listVideoTemplatesOptions(), enabled })

// useLookSample is the look sample of a request, as an object URL. A request is keyed by its body: when the owner
// changes a field, the sample asked before is aborted (the query's signal, which the server takes as a client
// cancel that kills its ffmpeg, row 14) and only the latest request's answer is shown; until it comes, the last
// sample stays. A refused sample is not asked again by itself: its message is shown.
export function useLookSample(body: VideoPreview, enabled: boolean) {
  const read = useQuery({
    queryKey: ["videoLookSample", body],
    queryFn: async ({ signal }) =>
      (await previewVideoLook({ body, parseAs: "blob", signal, throwOnError: true })).data as Blob,
    enabled,
    retry: false,
    staleTime: Infinity,
    gcTime: 60_000,
    placeholderData: keepPreviousData,
  })
  const url = useObjectURL(read.data)

  return { url, error: read.error, loading: read.isFetching }
}
