import { useEffect, useState } from "react"

// useNow ticks every second while active, for the durations a page shows (the Team page's, a docked Task's).
export function useNow(active = true) {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)

    return () => window.clearInterval(timer)
  }, [active])

  return now
}
