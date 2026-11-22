// The control plane's client API as this plugin uses it: the board, the open questions, an answer, and the event
// stream (api/openapi.yaml: ListTasks, ListQuestions, AnswerQuestion, StreamEvents). The worker's own fetch reaches
// it: Orca forks the worker as a Node process with the user's access
// (orca src/main/plugins/workers/parent-process/plugin-host-process.ts).

import type { Board, Question } from './waiting'

export type Answer = { option?: number; text?: string }

// ApiError is an answer that is not 2xx, with the client API's Error code (conflict, unauthorized, ...).
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export class ControlPlane {
  private readonly base: string
  private readonly token: string

  constructor(server: string, token: string) {
    this.base = `${server.replace(/\/+$/, '')}/api/v1`
    this.token = token
  }

  async board(signal?: AbortSignal): Promise<Board> {
    return (await this.call('GET', '/tasks', undefined, signal)) as Board
  }

  async openQuestions(signal?: AbortSignal): Promise<Question[]> {
    const page = (await this.call('GET', '/questions?status=open', undefined, signal)) as {
      data: Question[]
    }

    return page.data
  }

  async question(questionId: string): Promise<Question> {
    return (await this.call('GET', `/questions/${encodeURIComponent(questionId)}`)) as Question
  }

  async answer(questionId: string, answer: Answer): Promise<Question> {
    return (await this.call(
      'POST',
      `/questions/${encodeURIComponent(questionId)}/answer`,
      answer
    )) as Question
  }

  // stream follows GET /events/stream after a cursor until the server ends it, calling onEvent with each event's
  // seq; opened tells that the stream answered. Parsed as the web's generated client parses it (@hey-api/openapi-ts,
  // MIT, web/src/api/generated/core/serverSentEvents.gen.ts): frames end at a blank line, id: is the seq. The
  // cursor goes as Last-Event-ID, which the server prefers over after (api/openapi.yaml).
  async stream(
    after: number,
    onEvent: (seq: number) => void,
    signal: AbortSignal,
    opened?: () => void
  ) {
    const res = await this.fetch(`${this.base}/events/stream?after=${after}`, {
      headers: { ...this.headers(), Accept: 'text/event-stream', 'Last-Event-ID': String(after) },
      signal
    })
    if (!res.ok || !res.body) throw await apiError(res)
    opened?.()

    const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
    let buffer = ''

    try {
      while (true) {
        const { done, value } = await reader.read()
        if (done) return

        buffer = (buffer + value).replace(/\r\n?/g, '\n')
        const frames = buffer.split('\n\n')
        buffer = frames.pop() ?? ''

        for (const frame of frames) {
          for (const line of frame.split('\n')) {
            if (!line.startsWith('id:')) continue

            const seq = Number(line.slice(3).trim())
            if (Number.isSafeInteger(seq)) onEvent(seq)
          }
        }
      }
    } finally {
      await reader.cancel().catch(() => {})
    }
  }

  // fetch is the global fetch with the token taken out of what it throws: Node's fetch repeats a header value it
  // refuses ("Bearer <token>" is an invalid header value), and the error reaches the panel and the log.
  private async fetch(url: string, init: RequestInit): Promise<Response> {
    try {
      return await fetch(url, init)
    } catch (e) {
      if (e instanceof Error && e.name === 'AbortError') throw e

      throw new Error(this.redact(describe(e)))
    }
  }

  private redact(text: string): string {
    return text.split(this.token).join('[token]')
  }

  private headers(): Record<string, string> {
    return { Authorization: `Bearer ${this.token}` }
  }

  private async call(
    method: string,
    path: string,
    body?: unknown,
    signal?: AbortSignal
  ): Promise<unknown> {
    const res = await this.fetch(`${this.base}${path}`, {
      method,
      headers:
        body === undefined
          ? this.headers()
          : { ...this.headers(), 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal
    })
    if (!res.ok) throw await apiError(res)

    return res.json()
  }
}

async function apiError(res: Response): Promise<ApiError> {
  const body = (await res.json().catch(() => undefined)) as
    | { code?: string; message?: string }
    | undefined

  return new ApiError(
    res.status,
    body?.code ?? 'unavailable',
    body?.message ?? `HTTP ${res.status}`
  )
}

// describe is an error's message with its cause's: Node's fetch says only "fetch failed" and keeps the reason in
// its cause.
function describe(e: unknown): string {
  if (!(e instanceof Error)) return String(e)

  return e.cause instanceof Error ? `${e.message}: ${e.cause.message}` : e.message
}
