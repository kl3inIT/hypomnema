import { useEffect, useMemo } from "react"

// useObjectURL is a blob: URL for bytes, revoked when they change or are no longer shown.
export function useObjectURL(blob: Blob | undefined) {
  const url = useMemo(() => (blob ? URL.createObjectURL(blob) : null), [blob])
  useEffect(() => () => (url ? URL.revokeObjectURL(url) : undefined), [url])

  return url
}
