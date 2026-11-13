import { ArrowLeft, ArrowRight, RotateCw } from "lucide-react"
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type PointerEvent,
} from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { errorText } from "@/features/machines/machines-state"

import {
  buttonOf,
  countClick,
  keyInput,
  mapToPage,
  modifiers,
  pressedButtonOf,
  textInput,
  wheelUnit,
  type Click,
  type Controller,
  type MouseButton,
  type PagePoint,
  type ViewInput,
  type Viewport,
} from "./stream"
import { createFramePainter, createViewClient, type ViewClient } from "./stream-client"

// A zero-width space kept in the hidden input, so that a phone's Backspace has something to delete.
const sentinel = "​"
// A touch that moved less than this is a tap.
const tapSlopPx = 8

type Ended = { kind: "gone" } | { kind: "refused"; message: string }

function usePageVisible() {
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden")

  useEffect(() => {
    const changed = () => setVisible(document.visibilityState !== "hidden")
    document.addEventListener("visibilitychange", changed)

    return () => document.removeEventListener("visibilitychange", changed)
  }, [])

  return visible
}

// BrowserView is one tab of the shared browser, watched and, with control, used (issue #226): the address row,
// the control button and the picture. It is one piece, so that it can sit on the Browser page or in a pane.
//
// The picture and its input are adapted from T3 Code's ServerBrowserSurface (apps/web/src/browser, MIT License,
// Copyright (c) 2026 T3 Tools Inc.) as OpenMausBot keeps it (src/components/browser/StreamSurface.tsx, Apache
// License 2.0): a canvas fitted with `object-contain`, pointer events mapped through the letterbox, one merged
// move and one merged wheel a frame, a hidden text area that holds the keys' focus, Shift+Escape to leave the
// page. The address row is T3 Code's PreviewChromeRow. The control button and what it says are OpenMausBot's
// (ChromeRow.tsx); control is taken only by that button, as in T3 Code. Left out, since the control plane sends
// none of it: the agent's pointer, dialogs, file choosers, downloads and the clipboard. The tab fills the pane as
// T3 Code's does: the pane's size goes to the control plane as it settles, which gives the tab its shape at least
// a desktop wide (OpenMausBot's fill), and the picture is scaled into the pane.
export function BrowserView({ tab, label }: { tab: string; label: string }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const input = useRef<HTMLTextAreaElement>(null)
  const client = useRef<ViewClient | null>(null)
  const viewport = useRef<Viewport | null>(null)
  const painted = useRef(false)
  const control = useRef<Controller | null>(null)

  const [connected, setConnected] = useState(false)
  const [hasFrame, setHasFrame] = useState(false)
  const [controller, setController] = useState<Controller | null>(null)
  const [url, setUrl] = useState("")
  const [ended, setEnded] = useState<Ended | null>(null)
  const [attempt, setAttempt] = useState(0)

  const pendingMove = useRef<ViewInput | null>(null)
  const pendingWheel = useRef<Extract<ViewInput, { type: "wheel" }> | null>(null)
  const frame = useRef<number | null>(null)
  const buttons = useRef(0)
  const lastClick = useRef<Click | null>(null)
  const clicks = useRef<Record<MouseButton, number>>({ left: 0, middle: 0, right: 0, none: 0 })
  const touch = useRef<{ id: number; x: number; y: number; panning: boolean } | null>(null)
  const keySent = useRef(false)

  const hint = useId()
  const you = controller === "you"
  const pageVisible = usePageVisible()
  // A page nobody looks at takes no frames, unless its owner holds the browser (OpenMausBot's BrowserPanel).
  const watching = pageVisible || you

  const send = useCallback((message: ViewInput) => client.current?.send(message) ?? false, [])

  const flush = useCallback(() => {
    frame.current = null
    const move = pendingMove.current
    const wheel = pendingWheel.current
    pendingMove.current = null
    pendingWheel.current = null
    if (move) send(move)
    if (wheel) send(wheel)
  }, [send])

  const schedule = useCallback(() => {
    frame.current ??= requestAnimationFrame(flush)
  }, [flush])

  const clearInput = useCallback(() => {
    if (frame.current !== null) cancelAnimationFrame(frame.current)
    frame.current = null
    pendingMove.current = null
    pendingWheel.current = null
    buttons.current = 0
    touch.current = null
    input.current?.blur()
  }, [])

  useEffect(() => {
    if (!watching || !canvas.current) return

    const painter = createFramePainter(canvas.current, () => {
      if (painted.current) return
      painted.current = true
      setHasFrame(true)
    })
    const opened = createViewClient(
      tab,
      {
        onConnected: (now) => {
          setConnected(now)
          if (!now) {
            control.current = null
            setController(null)
            clearInput()
          }
        },
        onFrame: (jpeg) => painter.paint(jpeg),
        onViewport: (size) => (viewport.current = size),
        onControl: (who) => {
          control.current = who
          setController(who)
          if (who !== "you") clearInput()
        },
        onUrl: setUrl,
        onGone: () => setEnded({ kind: "gone" }),
        onRefused: (message) => setEnded({ kind: "refused", message }),
      },
      errorText,
    )
    client.current = opened

    return () => {
      opened.stop()
      painter.stop()
      client.current = null
      painted.current = false
      control.current = null
      setConnected(false)
      setHasFrame(false)
      setController(null)
      clearInput()
    }
  }, [tab, attempt, watching, clearInput])

  // Observes the pane's size and sends it as it settles, at most every 150 ms (T3 Code's RESIZE_THROTTLE_MS).
  useEffect(() => {
    const pane = canvas.current?.parentElement
    if (!watching || !pane) return

    let timer: ReturnType<typeof setTimeout> | null = null
    let sent = ""
    const measure = () => {
      timer = null
      const rect = pane.getBoundingClientRect()
      const width = Math.round(rect.width)
      const height = Math.round(rect.height)
      if (width < 1 || height < 1 || sent === `${width}x${height}`) return
      sent = `${width}x${height}`
      client.current?.send({ type: "resize", width, height })
    }
    const observer = new ResizeObserver(() => {
      if (timer === null) timer = setTimeout(measure, sent === "" ? 0 : 150)
    })
    observer.observe(pane)

    return () => {
      observer.disconnect()
      if (timer !== null) clearTimeout(timer)
    }
  }, [tab, attempt, watching])

  const pagePoint = useCallback(
    (clientX: number, clientY: number, clamp: boolean): PagePoint | null => {
      const at = canvas.current
      if (!at || !viewport.current || !painted.current || control.current !== "you") return null

      return mapToPage(at.getBoundingClientRect(), at, viewport.current, clientX, clientY, clamp)
    },
    [],
  )

  const queueWheel = useCallback(
    (point: PagePoint, deltaX: number, deltaY: number, held: number) => {
      const pending = pendingWheel.current
      pendingWheel.current = {
        type: "wheel",
        x: point.x,
        y: point.y,
        deltaX: (pending?.deltaX ?? 0) + deltaX,
        deltaY: (pending?.deltaY ?? 0) + deltaY,
        modifiers: held,
      }
      schedule()
    },
    [schedule],
  )

  // Registered natively: React's wheel listener is passive and cannot stop the page behind from scrolling.
  useEffect(() => {
    const at = canvas.current
    if (!at) return

    const onWheel = (event: WheelEvent) => {
      const point = pagePoint(event.clientX, event.clientY, false)
      if (!point) return
      event.preventDefault()
      const unit = wheelUnit(event.deltaMode, viewport.current)
      queueWheel(point, event.deltaX * unit, event.deltaY * unit, modifiers(event))
    }
    at.addEventListener("wheel", onWheel, { passive: false })

    return () => at.removeEventListener("wheel", onWheel)
  }, [pagePoint, queueWheel])

  const press = (button: MouseButton, x: number, y: number, time: number) => {
    lastClick.current = countClick(lastClick.current, button, x, y, time)

    return lastClick.current.count
  }

  const handlePointer = (event: PointerEvent<HTMLCanvasElement>) => {
    if (control.current !== "you") return
    const { type, pointerId, clientX, clientY } = event
    const cancelled = type === "pointercancel"

    if (event.pointerType === "touch") {
      const held = touch.current
      if (cancelled) {
        if (held?.id === pointerId) touch.current = null

        return
      }

      const point = pagePoint(clientX, clientY, type === "pointermove")
      if (type === "pointerdown") {
        if (event.isPrimary)
          touch.current = { id: pointerId, x: clientX, y: clientY, panning: false }

        return
      }
      if (!held || held.id !== pointerId) return

      if (type === "pointermove") {
        if (!held.panning && Math.hypot(clientX - held.x, clientY - held.y) < tapSlopPx) return
        // A drag scrolls the page under the finger.
        if (point)
          queueWheel(point, (held.x - clientX) * point.scale, (held.y - clientY) * point.scale, 0)
        touch.current = { id: pointerId, x: clientX, y: clientY, panning: true }

        return
      }

      touch.current = null
      if (held.panning || !point) return
      // A tap is a click.
      flush()
      const clickCount = press("left", clientX, clientY, event.timeStamp)
      const at = { x: point.x, y: point.y, modifiers: 0 }
      send({ type: "mouse", action: "move", ...at, button: "none", clickCount: 0 })
      send({ type: "mouse", action: "down", ...at, button: "left", clickCount })
      send({ type: "mouse", action: "up", ...at, button: "left", clickCount })

      return
    }

    const previous = buttons.current
    const up = cancelled || type === "pointerup"
    if (up && previous === 0) return
    if (up) buttons.current = cancelled ? 0 : event.buttons & 7

    // A drag that left the picture stays on its edge, so that a release still reaches the tab.
    const point = pagePoint(
      clientX,
      clientY,
      type !== "pointerdown" && (up || previous !== 0 || event.buttons !== 0),
    )
    if (!point) return

    const at = { x: point.x, y: point.y, modifiers: modifiers(event) }
    const mouse = (action: "down" | "up", button: MouseButton) => {
      if (action === "down")
        clicks.current[button] = press(button, clientX, clientY, event.timeStamp)
      send({ type: "mouse", action, ...at, button, clickCount: clicks.current[button] })
    }

    if (type === "pointerdown") {
      input.current?.focus({ preventScroll: true })
      event.currentTarget.setPointerCapture(pointerId)
      flush()
      buttons.current = event.buttons & 7
      mouse("down", buttonOf(event.button))
    } else if (type === "pointerup") {
      flush()
      mouse("up", buttonOf(event.button))
    } else {
      // A second button pressed or let go during a drag arrives as a move. A cancel lets every button go.
      const next = cancelled ? 0 : event.buttons & 7
      const changed = previous ^ next
      if (previous !== 0 && changed !== 0) {
        flush()
        for (const bit of [1, 2, 4]) {
          if (changed & bit) mouse(next & bit ? "down" : "up", pressedButtonOf(bit))
        }
        buttons.current = next
      }
      if (cancelled) return

      pendingMove.current = {
        type: "mouse",
        action: "move",
        ...at,
        button: pressedButtonOf(event.buttons),
        clickCount: 0,
      }
      schedule()
    }
  }

  const resetInput = (textarea: HTMLTextAreaElement) => {
    keySent.current = false
    textarea.value = sentinel
    textarea.setSelectionRange(sentinel.length, sentinel.length)
  }

  const handleKey = (action: "down" | "up", event: KeyboardEvent<HTMLTextAreaElement>) => {
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

    // Escape and Tab alone belong to the page; Shift+Escape comes back to the app.
    if (event.key === "Escape" && event.shiftKey) {
      event.preventDefault()
      event.stopPropagation()
      if (action === "down") event.currentTarget.blur()

      return
    }

    const shortcut = event.ctrlKey || event.metaKey
    // A paste arrives as its own event, with this device's clipboard.
    if (shortcut && event.key.toLowerCase() === "v") return
    if (event.shiftKey && event.key === "Insert") return

    const message = keyInput(action, event)
    send(message)
    keySent.current =
      action === "down" &&
      !shortcut &&
      (("text" in message && message.text !== undefined) ||
        event.key === "Backspace" ||
        event.key === "Delete")

    // A shortcut also stays with the app; any other key is the page's alone.
    if (shortcut) return
    event.preventDefault()
    event.stopPropagation()
  }

  const sendText = (text: string) => textInput(text).forEach(send)
  const pressKey = (key: string, keyCode: number) => {
    send({ type: "key", action: "down", key, code: key, keyCode, modifiers: 0 })
    send({ type: "key", action: "up", key, code: key, keyCode, modifiers: 0 })
  }

  const handleInput = (event: FormEvent<HTMLTextAreaElement>) => {
    const native = event.nativeEvent
    if (native instanceof InputEvent && native.isComposing) return

    const textarea = event.currentTarget
    const kind = native instanceof InputEvent ? native.inputType : ""
    // A key already sent on its way down is not sent again; a phone's keyboard deletes and types here only.
    if (keySent.current) keySent.current = false
    else if (kind === "deleteContentBackward") pressKey("Backspace", 8)
    else if (kind === "deleteContentForward") pressKey("Delete", 46)
    else sendText(textarea.value.replace(sentinel, ""))
    resetInput(textarea)
  }

  const waiting = !ended && !(connected && hasFrame)

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="browser-view">
      <AddressRow
        url={url}
        you={you}
        controller={controller}
        onNavigate={(to) => send({ type: "navigate", url: to })}
        onHistory={(delta) => send({ type: "history", delta })}
        onReload={() => send({ type: "reload" })}
        onControl={() => send({ type: you ? "releaseControl" : "takeControl" })}
      />
      <div className="relative min-h-0 flex-1 bg-black">
        <canvas
          ref={canvas}
          aria-hidden="true"
          className={`block size-full touch-none object-contain ${you ? "cursor-default" : "cursor-not-allowed"}`}
          onPointerDown={handlePointer}
          onPointerMove={handlePointer}
          onPointerUp={handlePointer}
          onPointerCancel={handlePointer}
          // Keeps the focus in the input below and stops the page's own text selection.
          onMouseDown={(event) => event.preventDefault()}
          onContextMenu={(event) => event.preventDefault()}
        />
        {/* Where the page's keys are typed. In the corner, so that focusing it never scrolls; 16px keeps a phone
            from zooming in on it. */}
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
          onKeyDown={(event) => handleKey("down", event)}
          onKeyUp={(event) => handleKey("up", event)}
          onInput={handleInput}
          onCompositionEnd={(event) => {
            sendText(event.data.replace(sentinel, ""))
            resetInput(event.currentTarget)
          }}
          // Copying the input would put its sentinel on this device's clipboard.
          onCopy={(event) => event.preventDefault()}
          onCut={(event) => event.preventDefault()}
          onPaste={(event) => {
            event.preventDefault()
            sendText(event.clipboardData.getData("text/plain"))
          }}
        />
        <span id={hint} className="sr-only">
          Your keys go to the page. Press Shift+Escape to come back.
        </span>
        {waiting && (
          <output className="pointer-events-none absolute inset-0 flex items-center justify-center gap-2 text-sm text-muted-foreground">
            <Spinner />
            Connecting…
          </output>
        )}
        {ended && (
          <div
            role="alert"
            className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-background/95 p-6 text-center text-sm"
          >
            <p className="max-w-sm text-muted-foreground">
              {ended.kind === "gone" ? "This tab is closed." : ended.message}
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setEnded(null)
                setAttempt((n) => n + 1)
              }}
            >
              Reconnect
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}

// AddressRow is the row above the picture: back, forward, reload, the address, and who controls. Everything but
// the control button needs control.
function AddressRow({
  url,
  you,
  controller,
  onNavigate,
  onHistory,
  onReload,
  onControl,
}: {
  url: string
  you: boolean
  controller: Controller | null
  onNavigate: (url: string) => void
  onHistory: (delta: number) => void
  onReload: () => void
  onControl: () => void
}) {
  const [draft, setDraft] = useState("")
  const [editing, setEditing] = useState(false)
  const field = useRef<HTMLInputElement>(null)
  const shown = url === "about:blank" ? "" : url

  return (
    <div className="flex h-11 shrink-0 items-center gap-1 border-b px-2">
      <RowButton label="Back" disabled={!you} onClick={() => onHistory(-1)}>
        <ArrowLeft />
      </RowButton>
      <RowButton label="Forward" disabled={!you} onClick={() => onHistory(1)}>
        <ArrowRight />
      </RowButton>
      <RowButton label="Reload" disabled={!you} onClick={onReload}>
        <RotateCw />
      </RowButton>
      <form
        className="min-w-0 flex-1"
        onSubmit={(event) => {
          event.preventDefault()
          const to = draft.trim()
          if (to) onNavigate(to)
          field.current?.blur()
        }}
      >
        <Input
          ref={field}
          aria-label="Address"
          placeholder={you ? "Search or enter address" : "Take control to go to an address"}
          disabled={!you}
          value={editing ? draft : shown}
          onChange={(event) => setDraft(event.target.value)}
          onFocus={() => {
            setDraft(shown)
            setEditing(true)
            queueMicrotask(() => field.current?.select())
          }}
          onBlur={() => setEditing(false)}
          onKeyDown={(event) => {
            if (event.key !== "Escape") return
            setDraft(shown)
            field.current?.blur()
          }}
        />
      </form>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant={you ? "default" : "outline"}
              size="sm"
              disabled={controller === null}
              aria-pressed={you}
              onClick={onControl}
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
            ? "Every agent's browser tools are paused while you have control. Control goes back after five minutes without input."
            : "Take control to click and type. Every agent's browser tools pause meanwhile."}
        </TooltipContent>
      </Tooltip>
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
  children: React.ReactNode
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
