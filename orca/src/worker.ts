// The plugin's worker: it follows the control plane's events and keeps what waits for the owner in step, as the web's
// Work page does (web/src/features/work/use-work.ts, useLiveUpdates): a snapshot first, then the stream from the
// snapshot's cursor, a refresh 250 ms after an event, the stream reopened 2 s after it ends or fails. Each new item
// is notified once, by its key (Orca's notification-delivery-service.ts announces keyed news once; Omnara dedupes
// webhooks by id); the keys notified are kept in storage so a restart does not repeat them. A status bar item
// counts the items, so Orca keeps this worker running while the plugin is enabled.
import type { OrcaWorkerApi } from 'orca/worker'
import type { AnswerResult, InboxState } from './protocol'
import { ApiError, ControlPlane } from './client'
import { waitingFor, type Item, type Kind } from './waiting'

// SERVER is the control plane's URL (settings:own, declared in orca-plugin.json); TOKEN is the owner token
// (secrets:own); NOTIFIED is the keys already notified (storage:own).
const SERVER = 'server'
const TOKEN = 'token'
const NOTIFIED = 'notified'
const STATUS_ITEM = 'waiting'

// settle gathers a burst of events into one refresh; reopenAfter is the pause before the stream is opened again.
const settle = 250
const reopenAfter = 2000

const titles: Record<Kind, string> = {
  question: 'A worker asks you',
  review: 'A review waits for you',
  failed: 'A Task failed',
  blocked: 'A Task is blocked'
}

let stop: (() => void) | undefined

export default function activate(orca: OrcaWorkerApi): void {
  let state: InboxState = { server: '', hasToken: false, status: 'unconfigured', items: [] }
  let controller: AbortController | undefined
  let client: ControlPlane | undefined
  let announced = Promise.resolve()

  // publish leaves error out once it is cleared: a post carries JSON only, which has no undefined.
  const publish = (next: Partial<InboxState>) => {
    const { error, ...rest } = { ...state, ...next }
    state = error === undefined ? rest : { ...rest, error }
    orca.panels.post('inbox.changed', state)
  }

  const count = (n: number) =>
    orca.host
      .call('statusBar.update', {
        id: STATUS_ITEM,
        text: String(n),
        tone: n > 0 ? 'warning' : 'neutral',
        visible: n > 0
      })
      .catch((e: unknown) => orca.log(`status bar: ${messageOf(e)}`))

  // announce notifies the items whose key was never notified, then keeps the keys of the items there are now, so
  // a key whose item left is dropped. One at a time: two refreshes must not notify one item twice.
  const announce = (items: Item[]) => {
    announced = announced
      .then(async () => {
        const stored = await orca.storage.get(NOTIFIED)
        const known = new Set(
          Array.isArray(stored) ? stored.filter((k) => typeof k === 'string') : []
        )

        for (const item of items) {
          if (known.has(item.key)) continue

          await orca.host
            .call('notifications.show', {
              title: titles[item.kind],
              body: clip(`${item.text}\n${item.run} · ${item.task}`)
            })
            .catch((e: unknown) => orca.log(`notification: ${messageOf(e)}`))
        }

        await orca.storage.set(
          NOTIFIED,
          items.map((i) => i.key)
        )
      })
      .catch((e: unknown) => orca.log(`notified keys: ${messageOf(e)}`))
  }

  const show = (items: Item[]) => {
    publish({ items })
    void count(items.length)
    void announce(items)
  }

  async function follow(cp: ControlPlane, signal: AbortSignal) {
    let cursor: number | undefined
    let timer: ReturnType<typeof setTimeout> | undefined

    const fail = (e: unknown) => {
      if (signal.aborted) return
      orca.log(`control plane: ${messageOf(e)}`)
      publish({ status: 'error', error: messageOf(e) })
    }
    const refresh = async () => {
      const [board, questions] = await Promise.all([cp.board(signal), cp.openQuestions(signal)])
      if (!signal.aborted) show(waitingFor(board, questions))

      return board.after
    }
    const later = () => {
      clearTimeout(timer)
      timer = setTimeout(() => void refresh().catch(fail), settle)
    }
    signal.addEventListener('abort', () => clearTimeout(timer))

    while (!signal.aborted) {
      try {
        cursor ??= await refresh()
        await cp.stream(
          cursor,
          (seq) => {
            cursor = Math.max(cursor ?? seq, seq)
            later()
          },
          signal,
          () => publish({ status: 'live', error: undefined })
        )
      } catch (e) {
        fail(e)
      }

      await sleep(reopenAfter, signal)
    }
  }

  // restart follows the control plane the settings and secrets name now, after stopping the one before.
  async function restart() {
    controller?.abort()
    const c = new AbortController()
    controller = c

    const server = String((await orca.settings.get(SERVER)) ?? '').trim()
    const token = await orca.secrets.get(TOKEN)
    if (c.signal.aborted) return

    if (!server || !token) {
      client = undefined
      publish({ server, hasToken: !!token, status: 'unconfigured', error: undefined, items: [] })
      void count(0)

      return
    }

    client = new ControlPlane(server, token)
    publish({ server, hasToken: true, status: 'connecting', error: undefined })
    void follow(client, c.signal)
  }

  stop = () => controller?.abort()

  orca.panels.handle('inbox.get', () => state)

  orca.panels.handle('inbox.connect', async ({ server, token }) => {
    const url = server.trim().replace(/\/+$/, '')
    if (!/^https?:\/\/[^/]/.test(url)) throw coded('invalid_params', 'The server is an http(s) URL')

    await orca.settings.set(SERVER, url)
    if (token?.trim()) await orca.secrets.set(TOKEN, token.trim())
    await restart()

    return state
  })

  orca.panels.handle(
    'inbox.answer',
    async ({ questionId, option, text }): Promise<AnswerResult> => {
      if (!client) return { outcome: 'failed', message: 'Not connected to a control plane' }

      try {
        await client.answer(questionId, { option, text })

        return { outcome: 'answered' }
      } catch (e) {
        // The first answer wins: another is a conflict, as is an answer to a canceled question.
        if (e instanceof ApiError && e.code === 'conflict')
          return { outcome: 'taken', message: e.message }

        return { outcome: 'failed', message: messageOf(e) }
      }
    }
  )

  orca.settings.onDidChange(({ key }) => {
    if (key === SERVER) void restart().catch((e: unknown) => orca.log(`restart: ${messageOf(e)}`))
  })

  void restart().catch((e: unknown) => orca.log(`start: ${messageOf(e)}`))
}

export function deactivate(): void {
  stop?.()
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    signal.addEventListener('abort', () => (clearTimeout(timer), resolve()), { once: true })
  })
}

function coded(code: string, message: string): Error {
  return Object.assign(new Error(message), { code })
}

function clip(text: string): string {
  return text.length > 200 ? `${text.slice(0, 199)}…` : text
}

// messageOf says what went wrong; Node's fetch says only "fetch failed" and keeps the reason in its cause.
function messageOf(e: unknown): string {
  if (!(e instanceof Error)) return String(e)

  return e.cause instanceof Error ? `${e.message}: ${e.cause.message}` : e.message
}
