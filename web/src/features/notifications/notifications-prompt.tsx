import { Link } from "@tanstack/react-router"
import { Bell } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/button"

import { TurnOnButton } from "./notifications-screen"
import { usePush } from "./use-push"

// dismissedKey keeps "Not now" in this browser, so the prompt is asked once.
const dismissedKey = "hypomnema.notifications-prompt.dismissed"

const dismissed = () => {
  try {
    return localStorage.getItem(dismissedKey) === "1"
  } catch {
    return false
  }
}

// NotificationsPrompt offers notifications once, where something waits on the owner (issue #1047): permission is
// asked only when he presses the button, as the owner's Second Bite and OpenClaw ask it (OpenClaw also on the first
// chat send). "Not now" hides it in this browser; Settings, Notifications keeps the switch.
export function NotificationsPrompt({ waiting }: { waiting: boolean }) {
  if (!waiting) return null

  return <Prompt />
}

function Prompt() {
  const push = usePush()
  const [hidden, setHidden] = useState(dismissed)

  const ready = push.devices.data && push.endpoint !== undefined
  if (hidden || !ready || push.here || push.support === "unsupported") return null
  if (push.permission === "denied") return null

  return (
    <aside
      aria-label="Turn on notifications"
      className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border bg-card px-3 py-2 text-sm"
    >
      <Bell aria-hidden className="size-4 shrink-0 text-muted-foreground" />
      <p className="min-w-0 flex-1">
        Get a notification when something waits on you, even with the app closed.
      </p>
      <div className="flex gap-2">
        {push.support === "install" ? (
          <Button render={<Link to="/settings/notifications" />} size="sm">
            How to turn on
          </Button>
        ) : (
          <TurnOnButton push={push} label="Turn on" />
        )}
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            try {
              localStorage.setItem(dismissedKey, "1")
            } catch {
              // A browser that keeps nothing asks again next time.
            }
            setHidden(true)
          }}
        >
          Not now
        </Button>
      </div>
    </aside>
  )
}
