import { SendHorizontal, Square } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"

// The message box. Enter sends and Shift+Enter breaks the line, but never while an input method is composing, so
// Vietnamese with Telex or a Japanese IME is not sent half typed (assistant-ui's composer and pi-web's ChatInput
// check the same). While pi works, Stop asks it to end its turn; a message sent then reaches pi as a follow-up.
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

  return (
    <form
      className="flex items-end gap-2 border-t p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]"
      onSubmit={(event) => {
        event.preventDefault()
        void send()
      }}
    >
      <Textarea
        aria-label="Message"
        placeholder={working ? "Add to what pi is doing" : "Describe the work"}
        rows={1}
        value={text}
        disabled={disabled}
        onChange={(event) => setText(event.target.value)}
        onKeyDown={(event) => {
          if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return
          event.preventDefault()
          void send()
        }}
        className="max-h-40 min-h-10 resize-none"
      />
      {working && (
        <Button type="button" size="icon" variant="outline" aria-label="Stop" onClick={onStop}>
          <Square />
        </Button>
      )}
      <Button
        type="submit"
        size="icon"
        aria-label="Send"
        disabled={disabled || sending || !text.trim()}
      >
        <SendHorizontal />
      </Button>
    </form>
  )
}
