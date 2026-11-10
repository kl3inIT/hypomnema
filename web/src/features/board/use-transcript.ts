import { useCallback, useEffect, useRef, useState } from "react"

import "@/api/client"
import { listDispatchItems } from "@/api/generated/sdk.gen"
import type { TranscriptItem } from "@/api/generated/types.gen"

// page is how many items one read brings (Omnara's history page).
const page = 100
// wait is how long one request waits for the next item; the server allows 50 s.
const wait = 25
// retryAfter is the pause after a failed read.
const retryAfter = 3000
// idleAfter is the pause after an empty answer that did not wait (a server shutting down, a proxy that cut the
// wait), so the page never asks in a tight loop.
const idleAfter = 1000
// newest is a seq above every item, to read a transcript from its end.
const newest = Number.MAX_SAFE_INTEGER

const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

type State = { items: TranscriptItem[]; earlier: boolean; loaded: boolean }

// useTranscript reads a Dispatch's transcript from its end and follows it: the newest page first, then each item
// as the node sends it, by waiting on GET /dispatches/{id}/items as the CLI waits on a chat. It follows a settled
// Dispatch too: the last items arrive after the worker ended. Remount (key) for another Dispatch.
export function useTranscript(dispatchId: string) {
  const [state, setState] = useState<State>({ items: [], earlier: false, loaded: false })
  const [loadingEarlier, setLoadingEarlier] = useState(false)
  const first = useRef<number | undefined>(undefined)

  useEffect(() => {
    const controller = new AbortController()
    const { signal } = controller
    const path = { dispatch_id: dispatchId }
    let after: number | undefined

    async function follow() {
      while (!signal.aborted) {
        try {
          if (after === undefined) {
            const { data } = await listDispatchItems({
              path,
              query: { before: newest, limit: page },
              signal,
            })
            if (signal.aborted) return
            after = data.data.at(-1)?.seq ?? 0
            first.current = data.data[0]?.seq
            setState({ items: data.data, earlier: data.has_more, loaded: true })
            continue
          }

          const asked = Date.now()
          const { data } = await listDispatchItems({
            path,
            query: { after, wait, limit: page },
            signal,
          })
          if (signal.aborted) return
          const last = data.data.at(-1)
          if (!last) {
            if (Date.now() - asked < idleAfter) await pause(idleAfter)
            continue
          }

          after = last.seq
          first.current ??= data.data[0]?.seq
          setState((s) => ({ ...s, items: [...s.items, ...data.data] }))
        } catch {
          if (signal.aborted) return
          await pause(retryAfter)
        }
      }
    }

    void follow()

    return () => controller.abort()
  }, [dispatchId])

  // loadEarlier brings the page before the oldest item shown.
  const loadEarlier = useCallback(async () => {
    if (first.current === undefined) return
    setLoadingEarlier(true)

    try {
      const { data } = await listDispatchItems({
        path: { dispatch_id: dispatchId },
        query: { before: first.current, limit: page },
      })
      first.current = data.data[0]?.seq ?? first.current
      setState((s) => ({ ...s, items: [...data.data, ...s.items], earlier: data.has_more }))
    } catch {
      // The button stays, to try again.
    } finally {
      setLoadingEarlier(false)
    }
  }, [dispatchId])

  return { ...state, loadEarlier, loadingEarlier }
}
