import { SendHorizontal } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"

// The chat with the coordinator. Until the coordinator and its API exist, the screen says so and the message box
// is disabled rather than pretending to send.
export function ChatScreen() {
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col">
      <section className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
        <h1 className="text-lg font-medium">What should be built?</h1>
        <p className="text-muted-foreground">
          The coordinator is not connected yet. Runs start from the command line for now.
        </p>
      </section>
      <form
        className="flex items-end gap-2 border-t p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]"
        onSubmit={(event) => event.preventDefault()}
      >
        <Textarea
          aria-label="Message"
          placeholder="Describe the work"
          rows={1}
          disabled
          className="max-h-40 min-h-10 resize-none"
        />
        <Button type="submit" size="icon" aria-label="Send" disabled>
          <SendHorizontal />
        </Button>
      </form>
    </div>
  )
}
