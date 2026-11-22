// A stand-in for the control plane's client API, behind the global fetch the worker uses: the board, the open
// questions, answers and an event stream the test writes to. Test code only; nothing the plugin loads imports it.
import type { Board, Question } from './waiting'

type Answered = { id: string; body: unknown; auth: string | null }

export function fakeControlPlane(board: Board, questions: Question[] = []) {
  const encoder = new TextEncoder()
  const streams = new Map<ReadableStreamDefaultController<Uint8Array>, number>()
  const frame = (seq: number) =>
    encoder.encode(`id: ${seq}\nevent: task.status\ndata: {"seq":${seq}}\n\n`)
  const cp = {
    board,
    questions,
    answers: [] as Answered[],
    // What POST /questions/{id}/answer says: 200, or 409 for a question answered or canceled meanwhile.
    answerStatus: 200,
    // The Last-Event-ID each stream was opened with, and each path read, in order.
    opened: [] as (string | null)[],
    reads: [] as string[],
    // The events so far: a stream sends those after its cursor, as GET /events/stream does
    // (internal/controlplane/clientapi/events.go).
    seq: board.after,
    // While set, GET /tasks waits for it before it reads anything, as a request the server has not reached yet.
    holdBoard: undefined as Promise<void> | undefined,
    // How many reads of GET /tasks answer 500 from now.
    failBoard: 0,
    // emit records a new event and sends it on every open stream.
    emit() {
      cp.seq += 1
      for (const s of streams.keys()) s.enqueue(frame(cp.seq))

      return cp.seq
    },
    restore: () => {}
  }

  const json = (status: number, body: unknown) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

  const fake = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const url = new URL(String(input))
    // Headers refuse a value a header cannot carry, as fetch does before it sends anything.
    const headers = new Headers(init.headers)
    if (headers.get('Authorization') !== 'Bearer tok') {
      return json(401, { code: 'unauthorized', message: 'unauthorized' })
    }

    const path = url.pathname.replace(/^\/api\/v1/, '')
    cp.reads.push(path)
    if (path === '/tasks') {
      if (cp.holdBoard) await cp.holdBoard
      if (cp.failBoard > 0) {
        cp.failBoard -= 1

        return json(500, { code: 'internal_error', message: 'the database is locked' })
      }

      // The server reads the newest event's seq first, then the Tasks (clientapi/handlers.go, ListTasks).
      return json(200, { ...cp.board, after: cp.seq })
    }
    if (path === '/questions')
      return json(200, { data: cp.questions.filter((q) => q.status === 'open') })

    const one = /^\/questions\/([^/]+)$/.exec(path)
    if (one?.[1]) {
      const q = cp.questions.find((x) => x.id === one[1])

      return q ? json(200, q) : json(404, { code: 'not_found', message: 'question not found' })
    }

    const answer = /^\/questions\/([^/]+)\/answer$/.exec(path)
    if (answer?.[1] && init.method === 'POST') {
      cp.answers.push({
        id: answer[1],
        body: JSON.parse(String(init.body)),
        auth: headers.get('Authorization')
      })
      if (cp.answerStatus !== 200) {
        const q = cp.questions.find((x) => x.id === answer[1])
        // The server's words (orchestration/questions.go, AnswerQuestion).
        const message =
          q?.status === 'canceled'
            ? `conflict: question ${answer[1]} was canceled (task_canceled)`
            : `conflict: question ${answer[1]} already has another answer`

        return json(cp.answerStatus, { code: 'conflict', message })
      }

      return json(200, cp.questions.find((q) => q.id === answer[1]) ?? {})
    }

    if (path === '/events/stream') {
      const after = Number(headers.get('Last-Event-ID') ?? url.searchParams.get('after') ?? 0)
      cp.opened.push(headers.get('Last-Event-ID'))
      let self: ReadableStreamDefaultController<Uint8Array> | undefined
      const body = new ReadableStream<Uint8Array>({
        start(c) {
          self = c
          streams.set(c, after)
          c.enqueue(encoder.encode(': ok\n\n'))
          for (let seq = after + 1; seq <= cp.seq; seq++) c.enqueue(frame(seq))
        },
        cancel() {
          if (self) streams.delete(self)
        }
      })
      init.signal?.addEventListener('abort', () => {
        if (!self) return
        streams.delete(self)
        self.error(new DOMException('aborted', 'AbortError'))
      })

      return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
    }

    return json(404, { code: 'not_found', message: path })
  }

  const real = globalThis.fetch
  globalThis.fetch = fake as typeof fetch
  cp.restore = () => {
    globalThis.fetch = real
  }

  return cp
}

// until waits for what the worker does in the background, which no act of the test kit awaits.
export async function until(done: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 500; i++) {
    if (done()) return
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
  throw new Error(`timed out waiting for ${what}`)
}

// gate is a promise the test resolves when it chooses.
export function gate(): { wait: Promise<void>; open: () => void } {
  let open = () => {}
  const wait = new Promise<void>((resolve) => (open = resolve))

  return { wait, open }
}
