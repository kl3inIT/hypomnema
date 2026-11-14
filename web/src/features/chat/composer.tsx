import { CircleAlert, Paperclip, X } from "lucide-react"
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react"

import type { ChatAttachment } from "@/api/generated/types.gen"
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import { AttachmentStrip } from "./attachment-strip"
import type { Attachments } from "./attachments"
import { clearDraft, readDraft, saveDrafts, writeDraft } from "./drafts"
import { recall, type Walk } from "./recall"

// The message box, after t3code's composer (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code): chat/ComposerSurface.tsx for the raised card with its glass and outline,
// chat/ChatComposer.tsx for the body and footer, chat/ComposerPrimaryActions.tsx for the round Send and Stop.
// Enter sends and Shift+Enter breaks the line, never while an input method composes, so Vietnamese with Telex
// is not sent half typed. While pi works and the box is empty, Stop takes Send's place; a message sent then
// reaches pi as a follow-up. The box empties as the message leaves, which the chat shows at once; a send that fails
// puts the text back to be sent again and says why above the box (t3code's ChatView send path and
// ThreadErrorBanner.tsx).
//
// Files are attached (issue #343; on the New chat page too, issue #357) with the paperclip, by pasting them or by
// dropping them on the chat, as t3code's composer takes them (useComposerAttachmentIntake.ts): each is a chip above the box while it
// uploads, and Send waits for the uploads, then sends the text with the files (useComposerSend.ts). A file that is
// refused, or was not uploaded, is said above the box.
//
// controls sit in the footer left of Send, as t3code's footer holds its model and effort pickers (issue #369).
//
// What is written, with the files that are up, is kept as the chat's draft (issue #433, drafts.ts) and is there
// again when the owner comes back to the chat or reloads; it goes when it is sent. ↑ in the empty box brings back
// what this chat sent, newest first, and ↓ goes back to the empty box (recall.ts).
export function Composer({
  onSend,
  onStop,
  working,
  disabled,
  bot,
  attachments,
  controls,
  draft,
  sent,
}: {
  onSend: (text: string, files: ChatAttachment[]) => Promise<unknown>
  onStop: () => void
  working: boolean
  disabled?: boolean
  // bot names who the message goes to.
  bot: string
  // attachments are the files of the message; without them the composer takes none.
  attachments?: Attachments
  // controls are what the message is sent with: the chat's model and effort.
  controls?: ReactNode
  // draft is where the draft is kept, with the chat and Bot of a chat not made yet; without it none is.
  draft?: { key: string; chat?: string; bot?: string }
  // sent are the texts this chat sent, newest first, which ↑ brings back.
  sent?: readonly string[]
}) {
  const [text, setText] = useState(() => (draft ? readDraft(draft.key).text : ""))
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const picker = useRef<HTMLInputElement>(null)
  const box = useRef<HTMLTextAreaElement>(null)
  const walk = useRef<Walk>(null)
  // caret is where the caret goes once a message brought back is in the box.
  const caret = useRef<number | null>(null)
  const files = attachments?.items.length ?? 0
  const key = draft?.key
  const chat = draft?.chat
  const draftBot = draft?.bot
  const items = attachments?.items

  // The box's text as the draft keeps it.
  function type(next: string) {
    setText(next)
    if (key)
      writeDraft(key, { text: next, ...(chat && { chat }), ...(draftBot && { bot: draftBot }) })
  }

  // The files that are up are the draft's; those uploading or not uploaded are not kept.
  useEffect(() => {
    if (!key || !items) return
    const up = items
      .filter((item) => item.state === "done")
      .map((item) => ({
        name: item.name,
        path: item.path,
        size: item.size,
        ...(item.type && { type: item.type }),
      }))
    writeDraft(key, { files: up, ...(chat && { chat }), ...(draftBot && { bot: draftBot }) })
  }, [key, items, chat, draftBot])

  // The draft is saved as the composer goes, to another chat or page.
  useEffect(() => saveDrafts, [])

  useLayoutEffect(() => {
    const at = caret.current
    if (at === null || !box.current) return
    caret.current = null
    box.current.setSelectionRange(at, at)
  })

  async function send() {
    const message = text.trim()
    if ((!message && files === 0) || sending) return

    setSending(true)
    setError(null)
    attachments?.dismiss()
    walk.current = null
    type("")

    try {
      const files = attachments ? await attachments.draft.ready() : []
      await onSend(message, files)
      attachments?.draft.clear()
      if (key) {
        clearDraft(key)
        // What was typed while it was sent is the next draft.
        const typed = box.current?.value ?? ""
        if (typed !== "") type(typed)
      }
    } catch (err) {
      // What was typed meanwhile is kept; the failed text goes back only into an empty box.
      const typed = box.current?.value ?? ""
      if (typed === "") type(message)
      setError(`Not sent: ${errorText(err)}`)
    } finally {
      setSending(false)
    }
  }

  const stop = working && !text.trim() && files === 0
  // What is said above the box: a send that failed, else a file that was not taken.
  const problem = error ?? attachments?.problem ?? null

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
      {problem && (
        <div className="relative z-10 mb-2">
          <Alert variant="destructive">
            <CircleAlert />
            <AlertDescription>{problem}</AlertDescription>
            <AlertAction>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                aria-label="Dismiss"
                onClick={() => {
                  setError(null)
                  attachments?.dismiss()
                }}
              >
                <X />
              </Button>
            </AlertAction>
          </Alert>
        </div>
      )}
      <div className="relative z-10 w-full rounded-2xl shadow-composer after:pointer-events-none after:absolute after:inset-0 after:rounded-[inherit] after:border after:border-(--composer-outline) dark:shadow-none dark:after:inset-shadow-2xs dark:after:inset-shadow-[rgb(255_255_255/3%)]">
        {attachments && <AttachmentStrip attachments={attachments} />}
        <div className="relative px-4 pt-3.5 pb-2 sm:pt-4">
          <textarea
            aria-label="Message"
            placeholder={working ? `Add to what ${bot} is doing` : `Ask ${bot} to build something`}
            rows={1}
            value={text}
            disabled={disabled}
            ref={box}
            onChange={(event) => {
              walk.current = null
              type(event.target.value)
            }}
            onPaste={(event) => {
              // A pasted image, or files copied in the system's file manager, are attached; text pastes as text.
              const pasted = [...event.clipboardData.files]
              if (!attachments || pasted.length === 0) return
              event.preventDefault()
              attachments.attach(pasted)
            }}
            onKeyDown={(event) => {
              if (
                (event.key === "ArrowUp" || event.key === "ArrowDown") &&
                sent &&
                !event.altKey &&
                !event.ctrlKey &&
                !event.metaKey &&
                !event.shiftKey &&
                !event.nativeEvent.isComposing
              ) {
                const { selectionStart, selectionEnd } = event.currentTarget
                if (selectionStart !== selectionEnd) return
                const moved = recall(
                  walk.current,
                  event.key === "ArrowUp" ? "up" : "down",
                  text,
                  selectionStart,
                  sent,
                )
                if (!moved) return
                event.preventDefault()
                walk.current = moved.walk
                caret.current = moved.caret
                type(moved.text)
                // The same text again renders nothing, so the caret is put there now.
                if (moved.text === text) {
                  event.currentTarget.setSelectionRange(moved.caret, moved.caret)
                }

                return
              }
              if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return
              event.preventDefault()
              void send()
            }}
            className="-m-1 block field-sizing-content max-h-52 min-h-[calc(2lh+0.5rem)] w-[calc(100%+0.5rem)] resize-none overflow-y-auto bg-transparent p-1 text-base leading-relaxed text-foreground placeholder:text-muted-foreground focus:outline-none sm:text-sm"
          />
        </div>
        <div className="flex min-w-0 items-center justify-end gap-2 px-2 pb-2">
          {attachments && (
            <div className="me-auto">
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label="Attach files"
                title="Attach files"
                disabled={disabled}
                onClick={() => picker.current?.click()}
              >
                <Paperclip />
              </Button>
              <input
                ref={picker}
                type="file"
                multiple
                hidden
                aria-label="Files to attach"
                onChange={(event) => {
                  const picked = [...(event.target.files ?? [])]
                  event.target.value = ""
                  attachments.attach(picked)
                }}
              />
            </div>
          )}
          {controls && <div className="flex min-w-0 items-center gap-0.5">{controls}</div>}
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
              disabled={disabled || sending || (!text.trim() && files === 0)}
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

// errorText is the server's words for a refused send, or a plain line when it gave none (a network error).
function errorText(err: unknown) {
  if (err && typeof err === "object" && "message" in err && err.message) return String(err.message)

  return "the server could not be reached. Try again."
}
