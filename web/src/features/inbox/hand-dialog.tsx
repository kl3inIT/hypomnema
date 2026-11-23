import { Bot as BotIcon } from "lucide-react"
import { useId, useState } from "react"

import type { Todo } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import { Kbd } from "@/components/ui/kbd"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/toast"
import { errorText } from "@/lib/server-error"

import { quickPrompts } from "./inbox-thread"
import { useHandTodo } from "./use-inbox"

// The longest prompt the server takes, as a comment.
const maxPrompt = 10_000

// HandDialog hands a to-do to the main Bot with an optional prompt (issue #1111), as GitHub's "Assign to Copilot"
// asks for additional instructions: a multi-line box, quick prompts that fill it in one click, and Ctrl+Enter to hand
// it over. The item's quote, source, due and draft go with it on the server's side; the Bot is woken in a chat of
// its own for the item and decides what to do.
export function HandDialog({
  todo,
  bot,
  botName,
  onClose,
}: {
  todo: Todo
  bot?: string
  botName: string
  onClose: () => void
}) {
  const id = useId()
  const [prompt, setPrompt] = useState("")
  const hand = useHandTodo()

  const submit = () => {
    if (hand.isPending) return
    hand.mutate(
      { todo, bot, prompt },
      {
        onSuccess: () => {
          toast.add({ title: `Handed to ${botName}`, type: "success" })
          onClose()
        },
        onError: (error) =>
          toast.add({ title: "Not handed over", description: errorText(error), type: "error" }),
      },
    )
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <form
          autoComplete="off"
          className="contents"
          onSubmit={(event) => {
            event.preventDefault()
            submit()
          }}
        >
          <DialogHeader>
            <DialogTitle>Hand to {botName}</DialogTitle>
            <DialogDescription>
              {botName} gets “{todo.title}” with what was said, where it came from, its due date and
              its draft, in a chat of its own for this item. It does it, gives it to workers, or
              asks you here.
            </DialogDescription>
          </DialogHeader>
          <DialogPanel>
            <Field>
              <FieldLabel htmlFor={id}>What to do (optional)</FieldLabel>
              <Textarea
                id={id}
                value={prompt}
                rows={4}
                maxLength={maxPrompt}
                placeholder="Do it now, then draft a reply to Kai for me to send"
                onChange={(event) => setPrompt(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
                    event.preventDefault()
                    submit()
                  }
                }}
              />
            </Field>
            <fieldset className="mt-3 flex flex-wrap gap-1.5" aria-label="Quick prompts">
              {quickPrompts.map((p) => (
                <Button
                  key={p}
                  type="button"
                  size="xs"
                  variant="outline"
                  aria-pressed={prompt === p}
                  onClick={() => setPrompt(p)}
                >
                  {p}
                </Button>
              ))}
            </fieldset>
          </DialogPanel>
          <DialogFooter>
            <span className="me-auto hidden text-xs text-muted-foreground sm:inline">
              <Kbd>Ctrl</Kbd> <Kbd>Enter</Kbd> hands it over
            </span>
            <DialogClose render={<Button type="button" variant="ghost" />}>Cancel</DialogClose>
            <Button type="submit" focusableWhenDisabled={hand.isPending} disabled={hand.isPending}>
              {hand.isPending ? (
                <Spinner data-icon="inline-start" />
              ) : (
                <BotIcon data-icon="inline-start" />
              )}
              Hand over
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
