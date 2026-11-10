import { Link } from "@tanstack/react-router"
import { FitAddon } from "@xterm/addon-fit"
import { WebLinksAddon } from "@xterm/addon-web-links"
import { Terminal } from "@xterm/xterm"
import "@xterm/xterm/css/xterm.css"
import { ArrowLeft } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import "@/api/client"
import { openNodeTerminal } from "@/api/generated/sdk.gen"
import { Button } from "@/components/ui/button"

import { errorText } from "./machines-state"
import { closedBecause, retryIn, socketUrl, type TerminalStatus } from "./terminal-state"
import { useMachines } from "./use-machines"

// TerminalScreen is a shell on a machine (issue #116): a page that is only the terminal, as Coder's TerminalPage
// (behaviour followed, code not copied), drawn with xterm.js. The session's id is in the address, so a reload or a
// dropped connection comes back to the same shell, which the node keeps for five minutes with nobody watching. A
// lost connection is said above the terminal, which keeps what it showed, and is tried again by itself; a shell that
// ended, or a session another tab took over, waits for the owner.
export function TerminalScreen({
  nodeId,
  sessionId,
  onNewSession,
}: {
  nodeId: string
  sessionId: string
  onNewSession: () => void
}) {
  const host = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<TerminalStatus>({ kind: "connecting" })
  const [attempt, setAttempt] = useState(0)
  const name = useMachines().data?.find((n) => n.id === nodeId)?.name ?? nodeId

  useEffect(() => {
    if (!host.current) return

    const terminal = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 13,
      scrollback: 5000,
      // The terminal takes the page's own colours, which are the dark theme's here.
      theme: {
        background: getComputedStyle(host.current).backgroundColor,
        foreground: getComputedStyle(host.current).color,
      },
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.loadAddon(new WebLinksAddon())
    terminal.open(host.current)
    fit.fit()

    let socket: WebSocket | undefined
    let retry: ReturnType<typeof setTimeout> | undefined
    let tries = 0
    let left = false

    const send = (message: object) => {
      if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message))
    }
    const typed = terminal.onData((data) => send({ data }))
    const resized = terminal.onResize(({ rows, cols }) => send({ height: rows, width: cols }))
    const observer = new ResizeObserver(() => fit.fit())
    observer.observe(host.current)

    async function connect() {
      setStatus({ kind: "connecting" })

      let ticket: string
      try {
        ticket = (
          await openNodeTerminal({ path: { node_id: nodeId }, body: { session_id: sessionId } })
        ).data.ticket
      } catch (error) {
        if (!left) setStatus({ kind: "refused", message: errorText(error) })

        return
      }
      if (left) return

      const opened = new WebSocket(socketUrl(window.location, ticket, terminal.rows, terminal.cols))
      opened.binaryType = "arraybuffer"
      socket = opened
      opened.onopen = () => {
        tries = 0
        setStatus({ kind: "connected" })
        terminal.focus()
      }
      opened.onmessage = (event) => {
        if (event.data instanceof ArrayBuffer) terminal.write(new Uint8Array(event.data))
      }
      opened.onclose = (event) => {
        if (left) return

        const why = closedBecause(event.reason)
        if (why !== "dropped") {
          setStatus({ kind: why })

          return
        }

        // A dropped connection is tried again; the shell is still there for five minutes.
        const wait = retryIn(tries++)
        setStatus({ kind: "dropped", retryInSeconds: Math.round(wait / 1000) })
        retry = setTimeout(() => void connect(), wait)
      }
    }

    void connect()

    return () => {
      left = true
      clearTimeout(retry)
      socket?.close()
      observer.disconnect()
      typed.dispose()
      resized.dispose()
      terminal.dispose()
    }
  }, [nodeId, sessionId, attempt])

  return (
    <div className="dark flex h-svh w-full min-w-0 flex-col bg-background text-foreground">
      <header className="flex h-11 shrink-0 items-center gap-3 border-b px-3 text-sm">
        <Link
          to="/machines"
          aria-label="Back to machines"
          className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <ArrowLeft className="size-4" aria-hidden />
        </Link>
        <h1 className="min-w-0 truncate font-medium">{name}</h1>
        <span className="text-xs text-muted-foreground" data-testid="terminal-status">
          {statusText(status)}
        </span>
      </header>
      <Notice
        status={status}
        onRetry={() => setAttempt((n) => n + 1)}
        onNewSession={onNewSession}
      />
      <div
        ref={host}
        role="application"
        aria-label={`Terminal on ${name}`}
        className="min-h-0 flex-1 px-2 py-1"
      />
    </div>
  )
}

function statusText(status: TerminalStatus) {
  switch (status.kind) {
    case "connecting":
      return "Connecting…"
    case "connected":
      return "Connected"
    case "dropped":
      return "Disconnected"
    case "ended":
      return "Ended"
    case "replaced":
      return "Open elsewhere"
    case "refused":
      return "Not available"
  }
}

// Notice is the alert above the terminal when it is not connected: what happened and, where the owner decides,
// what to do next.
function Notice({
  status,
  onRetry,
  onNewSession,
}: {
  status: TerminalStatus
  onRetry: () => void
  onNewSession: () => void
}) {
  if (status.kind === "connected" || status.kind === "connecting") return null

  return (
    <div
      role="alert"
      className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-b bg-muted px-3 py-2 text-sm"
    >
      {status.kind === "dropped" && (
        <span>
          The connection dropped. Trying again in {status.retryInSeconds} s; the shell is kept for
          five minutes.
        </span>
      )}
      {status.kind === "ended" && (
        <>
          <span>The shell ended.</span>
          <Button variant="outline" size="sm" onClick={onNewSession}>
            New session
          </Button>
        </>
      )}
      {status.kind === "replaced" && (
        <>
          <span>This session was opened in another tab or window.</span>
          <Button variant="outline" size="sm" onClick={onRetry}>
            Use it here
          </Button>
        </>
      )}
      {status.kind === "refused" && (
        <>
          <span className="min-w-0 break-words">{status.message}</span>
          <Button variant="outline" size="sm" onClick={onRetry}>
            Try again
          </Button>
        </>
      )}
    </div>
  )
}
