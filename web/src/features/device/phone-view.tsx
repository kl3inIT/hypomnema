import { ChevronLeft, Circle, Smartphone, Square } from "lucide-react"
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from "react"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { errorText } from "@/features/machines/machines-state"

import {
  keyInput,
  screenPoint,
  stateLine,
  textInputs,
  type Controller,
  type PhoneInput,
  type PhoneState,
} from "./stream"
import { createPhoneClient, type PhoneClient } from "./stream-client"

// A zero-width space kept in the hidden input, so that a phone's Backspace has something to delete.
const sentinel = "​"

function usePageVisible() {
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden")

  useEffect(() => {
    const changed = () => setVisible(document.visibilityState !== "hidden")
    document.addEventListener("visibilitychange", changed)

    return () => document.removeEventListener("visibilitychange", changed)
  }, [])

  return visible
}

// PhoneView is the Android phone a node holds, watched and, with control, used (issue #417, step 3): a row with the
// phone's buttons and the control button, and the screen under it. It is one piece, as BrowserView is, so that it
// sits in a pane.
//
// Adapted from T3 Code's DeviceStreamView and its controls rail (apps/web/src/components/device/DeviceStreamView.tsx,
// DeviceControlsRail.tsx, DeviceLoadingView.tsx; MIT License, Copyright (c) 2026 T3 Tools Inc.): the screen fitted in
// the pane, one finger's pointer as touches from 0 to 1 of the screen, keys and a paste as Android keys and text,
// Home, Back and Recents, and the phone's card while the video is not there. Changed to the browser view's shape
// (../browser/browser-view.tsx): the buttons sit in a row above the screen, control is taken only with the button and
// the screen takes no input without it, moves are merged one a frame, a hidden text area holds the keys' focus with
// Shift+Escape to leave, and the stream stops while the page is hidden unless the owner holds the phone. Left out:
// T3 Code's 3D phone, its accessibility frames, rotation, folding and tools panel.
export function PhoneView({ label }: { label: string }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const input = useRef<HTMLTextAreaElement>(null)
  const client = useRef<PhoneClient | null>(null)
  const control = useRef<Controller | null>(null)
  const painted = useRef(false)
  const finger = useRef<number | null>(null)
  const pendingMove = useRef<PhoneInput | null>(null)
  const frame = useRef<number | null>(null)
  const keySent = useRef(false)

  const [connected, setConnected] = useState(false)
  const [hasFrame, setHasFrame] = useState(false)
  const [controller, setController] = useState<Controller | null>(null)
  const [state, setState] = useState<{ state: PhoneState; reason: string } | null>(null)
  const [trouble, setTrouble] = useState<string | null>(null)
  const [gone, setGone] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)

  const hint = useId()
  const you = controller === "you"
  const pageVisible = usePageVisible()
  // A page nobody looks at takes no frames, unless its owner holds the phone, as the browser's.
  const watching = pageVisible || you

  const send = useCallback((message: PhoneInput) => client.current?.send(message) ?? false, [])

  const flush = useCallback(() => {
    frame.current = null
    const move = pendingMove.current
    pendingMove.current = null
    if (move) send(move)
  }, [send])

  const clearInput = useCallback(() => {
    if (frame.current !== null) cancelAnimationFrame(frame.current)
    frame.current = null
    pendingMove.current = null
    finger.current = null
    input.current?.blur()
  }, [])

  useEffect(() => {
    if (!watching || !canvas.current) return

    const opened = createPhoneClient(
      canvas.current,
      {
        onConnected: (now) => {
          setConnected(now)
          if (!now) {
            control.current = null
            setController(null)
            clearInput()
          }
        },
        onControl: (who) => {
          control.current = who
          setController(who)
          if (who !== "you") clearInput()
        },
        onState: (next, reason) => {
          setState({ state: next, reason })
          if (next !== "running") {
            painted.current = false
            setHasFrame(false)
          }
        },
        onScreen: () => {
          setTrouble(null)
          painted.current = false
          setHasFrame(false)
        },
        onPainted: () => {
          if (painted.current) return
          painted.current = true
          setTrouble(null)
          setHasFrame(true)
        },
        onTrouble: setTrouble,
        onGone: setGone,
      },
      errorText,
    )
    client.current = opened

    return () => {
      opened.stop()
      client.current = null
      painted.current = false
      control.current = null
      setConnected(false)
      setHasFrame(false)
      setController(null)
      setState(null)
      setTrouble(null)
      clearInput()
    }
  }, [attempt, watching, clearInput])

  const pointAt = (clientX: number, clientY: number, clamp: boolean) => {
    const at = canvas.current
    if (!at || !painted.current || control.current !== "you") return null

    return screenPoint(at.getBoundingClientRect(), at, clientX, clientY, clamp)
  }

  // One finger at a time: the mouse's left button or the first touch, as T3 Code's view takes it.
  const handlePointer = (event: PointerEvent<HTMLCanvasElement>) => {
    if (control.current !== "you") return
    const { type, pointerId, clientX, clientY } = event

    if (type === "pointerdown") {
      if (finger.current !== null || (event.pointerType === "mouse" && event.button !== 0)) return
      const point = pointAt(clientX, clientY, false)
      if (!point) return
      input.current?.focus({ preventScroll: true })
      event.currentTarget.setPointerCapture(pointerId)
      finger.current = pointerId
      send({ type: "touch", action: "down", ...point })

      return
    }
    if (finger.current !== pointerId) return

    const point = pointAt(clientX, clientY, true)
    if (type === "pointermove") {
      if (!point) return
      pendingMove.current = { type: "touch", action: "move", ...point }
      if (frame.current === null) frame.current = requestAnimationFrame(flush)

      return
    }

    // Up, cancelled, or the capture lost: the finger lifts where it last was.
    flush()
    finger.current = null
    if (point) send({ type: "touch", action: "up", ...point })
  }

  const resetInput = (textarea: HTMLTextAreaElement) => {
    keySent.current = false
    textarea.value = sentinel
    textarea.setSelectionRange(sentinel.length, sentinel.length)
  }

  const handleKey = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (control.current !== "you") return
    keySent.current = false
    // A composed text and a phone's keyboard arrive as input events.
    if (
      event.nativeEvent.isComposing ||
      event.keyCode === 229 ||
      event.key === "Process" ||
      event.key === "Unidentified"
    ) {
      return
    }

    // Shift+Escape comes back to the app; Escape alone is the phone's Back.
    if (event.key === "Escape" && event.shiftKey) {
      event.preventDefault()
      event.currentTarget.blur()

      return
    }

    const shortcut = event.ctrlKey || event.metaKey
    // A paste arrives as its own event, with this device's clipboard.
    if (shortcut && event.key.toLowerCase() === "v") return
    if (event.shiftKey && event.key === "Insert") return

    const message = keyInput(event)
    if (!message) return
    send(message)
    keySent.current = true
    event.preventDefault()
    event.stopPropagation()
  }

  const handleInput = (event: FormEvent<HTMLTextAreaElement>) => {
    const native = event.nativeEvent
    if (native instanceof InputEvent && native.isComposing) return

    const textarea = event.currentTarget
    const kind = native instanceof InputEvent ? native.inputType : ""
    // A key already sent on its way down is not sent again; a phone's keyboard deletes and types here only.
    if (keySent.current) keySent.current = false
    else if (kind === "deleteContentBackward") send({ type: "key", keycode: 67 })
    else if (kind === "deleteContentForward") send({ type: "key", keycode: 112 })
    else textInputs(textarea.value.replace(sentinel, "")).forEach(send)
    resetInput(textarea)
  }

  const shown = connected && hasFrame && state?.state === "running" && !gone

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="phone-view">
      <div className="flex h-11 shrink-0 items-center gap-1 border-b px-2">
        <RowButton label="Back" disabled={!you} onClick={() => send({ type: "back" })}>
          <ChevronLeft />
        </RowButton>
        <RowButton label="Home" disabled={!you} onClick={() => send({ type: "home" })}>
          <Circle />
        </RowButton>
        <RowButton label="Recents" disabled={!you} onClick={() => send({ type: "recents" })}>
          <Square />
        </RowButton>
        <p className="ms-1 min-w-0 flex-1 truncate text-sm text-muted-foreground">
          {you ? "You are using the phone" : "Watching the phone"}
        </p>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant={you ? "default" : "outline"}
                size="sm"
                disabled={controller === null}
                aria-pressed={you}
                onClick={() => send({ type: you ? "releaseControl" : "takeControl" })}
              />
            }
          >
            <span className="hidden sm:inline">
              {controller === null
                ? "Connecting…"
                : you
                  ? "You have control ·"
                  : "Agents have control ·"}
            </span>
            {you ? "Hand back" : "Take control"}
          </TooltipTrigger>
          <TooltipContent>
            {you
              ? "Every agent's phone tools are paused while you have control. Control goes back after five minutes without input."
              : "Take control to tap and type, to sign in for the agents. Every agent's phone tools pause meanwhile."}
          </TooltipContent>
        </Tooltip>
      </div>
      <div className="relative min-h-0 flex-1 bg-black">
        <canvas
          ref={canvas}
          aria-hidden="true"
          className={`block size-full touch-none object-contain ${you ? "cursor-default" : "cursor-not-allowed"}`}
          onPointerDown={handlePointer}
          onPointerMove={handlePointer}
          onPointerUp={handlePointer}
          onPointerCancel={handlePointer}
          onLostPointerCapture={handlePointer}
          // Keeps the focus in the input below and stops the page's own text selection.
          onMouseDown={(event) => event.preventDefault()}
          onContextMenu={(event) => event.preventDefault()}
        />
        {/* Where the phone's keys are typed, as in the browser's view. */}
        <textarea
          ref={input}
          aria-label={label}
          aria-describedby={hint}
          aria-keyshortcuts="Shift+Escape"
          autoCapitalize="off"
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          disabled={!you}
          defaultValue={sentinel}
          className="sr-only top-0 left-0 text-base"
          onFocus={(event) => resetInput(event.currentTarget)}
          onKeyDown={handleKey}
          onInput={handleInput}
          onCompositionEnd={(event) => {
            textInputs(event.data.replace(sentinel, "")).forEach(send)
            resetInput(event.currentTarget)
          }}
          // Copying the input would put its sentinel on this device's clipboard.
          onCopy={(event) => event.preventDefault()}
          onCut={(event) => event.preventDefault()}
          onPaste={(event) => {
            event.preventDefault()
            textInputs(event.clipboardData.getData("text/plain")).forEach(send)
          }}
        />
        <span id={hint} className="sr-only">
          Your keys go to the phone; Escape is its Back. Press Shift+Escape to come back.
        </span>
        {!shown && !gone && (
          <PhoneCard>
            <p className="flex items-center gap-2 text-xs text-muted-foreground">
              {state?.state !== "failed" && <Spinner />}
              {trouble ?? (connected ? stateLine(state?.state ?? null) : "Connecting…")}
            </p>
            {state?.state === "failed" && state.reason && (
              <p className="text-xs text-muted-foreground">{state.reason}</p>
            )}
          </PhoneCard>
        )}
        {gone && (
          <PhoneCard alert>
            <p className="text-xs text-muted-foreground">
              {gone.charAt(0).toUpperCase() + gone.slice(1)}
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setGone(null)
                setAttempt((n) => n + 1)
              }}
            >
              Reconnect
            </Button>
          </PhoneCard>
        )}
      </div>
    </div>
  )
}

// PhoneCard is the phone's card over the screen while it is not there: T3 Code's DeviceLoadingView.
function PhoneCard({ alert, children }: { alert?: boolean; children: ReactNode }) {
  return (
    <div
      role={alert ? "alert" : "status"}
      className="absolute inset-0 flex items-center justify-center bg-background px-6 py-10"
    >
      <div className="flex max-w-sm flex-col items-center gap-3 text-center">
        <div className="grid size-12 place-items-center rounded-xl border bg-muted/30">
          <Smartphone className="size-6 text-muted-foreground" aria-hidden />
        </div>
        <p className="text-sm font-medium">Phone</p>
        {children}
      </div>
    </div>
  )
}

function RowButton({
  label,
  disabled,
  onClick,
  children,
}: {
  label: string
  disabled: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={label}
            disabled={disabled}
            onClick={onClick}
          />
        }
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}
