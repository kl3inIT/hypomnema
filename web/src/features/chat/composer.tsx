import { useState } from "react"

import { cn } from "@/lib/utils"

// The message box, after t3code's composer (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): chat/ComposerSurface.tsx for the raised card with its glass and outline,
// chat/ChatComposer.tsx for the body and footer, chat/ComposerPrimaryActions.tsx for the round Send and Stop.
// Enter sends and Shift+Enter breaks the line, never while an input method composes, so Vietnamese with Telex
// is not sent half typed. While pi works and the box is empty, Stop takes Send's place; a message sent then
// reaches pi as a follow-up.
export function Composer({
  onSend,
  onStop,
  working,
  disabled,
}: {
  onSend: (text: string) => Promise<unknown>
  onStop: () => void
  working: boolean
  disabled?: boolean
}) {
  const [text, setText] = useState("")
  const [sending, setSending] = useState(false)

  async function send() {
    const message = text.trim()
    if (!message || sending) return

    setSending(true)

    try {
      await onSend(message)
      setText("")
    } finally {
      setSending(false)
    }
  }

  const stop = working && !text.trim()

  return (
    <form
      className={cn(
        "relative isolate mx-auto w-full max-w-(--chat-content-max-width)",
        "[--composer-outline:rgb(0_0_0/8%)] [--composer-surface:var(--card)] dark:[--composer-outline:rgb(255_255_255/5%)] dark:[--composer-surface:var(--surface-raised)]",
        "before:pointer-events-none before:absolute before:inset-0 before:z-0 before:rounded-2xl before:bg-(--composer-surface)/(--glass-opacity) before:backdrop-blur-(--glass-blur)",
      )}
      onSubmit={(event) => {
        event.preventDefault()
        void send()
      }}
    >
      <div className="relative z-10 w-full rounded-2xl shadow-composer after:pointer-events-none after:absolute after:inset-0 after:rounded-[inherit] after:border after:border-(--composer-outline) dark:shadow-none dark:after:inset-shadow-2xs dark:after:inset-shadow-[rgb(255_255_255/3%)]">
        <div className="relative px-4 pt-3.5 pb-2 sm:pt-4">
          <textarea
            aria-label="Message"
            placeholder={working ? "Add to what pi is doing" : "Ask pi to build something"}
            rows={1}
            value={text}
            disabled={disabled}
            onChange={(event) => setText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return
              event.preventDefault()
              void send()
            }}
            className="-m-1 block field-sizing-content max-h-52 min-h-[calc(2lh+0.5rem)] w-[calc(100%+0.5rem)] resize-none overflow-y-auto bg-transparent p-1 text-base leading-relaxed text-foreground placeholder:text-muted-foreground focus:outline-none sm:text-sm"
          />
        </div>
        <div className="flex min-w-0 items-center justify-end gap-2 px-2 pb-2">
          {stop ? (
            <button
              type="button"
              aria-label="Stop"
              onClick={onStop}
              className="flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-full bg-destructive/90 text-white shadow-xs inset-shadow-control-highlight shadow-destructive/24 transition-colors duration-150 hover:bg-destructive active:shadow-none active:inset-shadow-control-pressed"
            >
              <svg
                width="12"
                height="12"
                viewBox="0 0 12 12"
                fill="currentColor"
                aria-hidden="true"
              >
                <rect x="2" y="2" width="8" height="8" rx="1.5" />
              </svg>
            </button>
          ) : (
            <button
              type="submit"
              aria-label="Send"
              disabled={disabled || sending || !text.trim()}
              className="relative isolate flex size-9 items-center justify-center overflow-hidden rounded-full bg-message-action text-message-action-foreground shadow-xs transition duration-150 hover:scale-105 hover:bg-message-action-hover active:shadow-none active:inset-shadow-control-pressed enabled:cursor-pointer enabled:inset-shadow-control-highlight enabled:shadow-message-action/24 disabled:pointer-events-none disabled:bg-foreground/12 disabled:text-muted-foreground disabled:shadow-none disabled:hover:scale-100 sm:size-8"
            >
              <svg
                width="14"
                height="14"
                viewBox="0 0 14 14"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                <path d="M7 11.5V2.5M7 2.5L3 6.5M7 2.5L11 6.5" />
              </svg>
            </button>
          )}
        </div>
      </div>
    </form>
  )
}
