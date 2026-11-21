// A stand-in for the control plane's client API, behind the global fetch the worker uses: the board, the open
// questions, answers and an event stream the test writes to. Test code only; nothing the plugin loads imports it.
import type { Board, Question } from './waiting'

type Answered = { id: string; body: unknown; auth: string | null }

export function fakeControlPlane(board: Board, questions: Question[] = []) {
  const encoder = new TextEncoder()
  const streams = new Set<ReadableStreamDefaultController<Uint8Array>>()
  const cp = {
    board,
    questions,
    answers: [] as Answered[],
    // What POST /questions/{id}/answer says: 200, or a refusal such as 409 conflict.
    answerStatus: 200,
    // The Last-Event-ID each stream was opened with.
    opened: [] as (string | null)[],
    emit(seq: number) {
      const frame = encoder.encode(`id: ${seq}\nevent: task.status\ndata: {"seq":${seq}}\n\n`)
      for (const s of streams) s.enqueue(frame)
    },
    restore: () => {}
  }

  const json = (status: number, body: unknown) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

  const fake = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const url = new URL(String(input))
    const headers = new Headers(init.headers)
    if (headers.get('Authorization') !== 'Bearer tok') {
      return json(401, { code: 'unauthorized', message: 'unauthorized' })
    }

    const path = url.pathname.replace(/^\/api\/v1/, '')
    if (path === '/tasks') return json(200, cp.board)
    if (path === '/questions') return json(200, { data: cp.questions })

    const answer = /^\/questions\/([^/]+)\/answer$/.exec(path)
    if (answer?.[1] && init.method === 'POST') {
      cp.answers.push({
        id: answer[1],
        body: JSON.parse(String(init.body)),
        auth: headers.get('Authorization')
      })
      if (cp.answerStatus !== 200) {
        return json(cp.answerStatus, {
          code: 'conflict',
          message: `question ${answer[1]} already has another answer`
        })
      }

      return json(200, cp.questions.find((q) => q.id === answer[1]) ?? {})
    }

    if (path === '/events/stream') {
      cp.opened.push(headers.get('Last-Event-ID'))
      let self: ReadableStreamDefaultController<Uint8Array> | undefined
      const body = new ReadableStream<Uint8Array>({
        start(c) {
          self = c
          streams.add(c)
          c.enqueue(encoder.encode(': ok\n\n'))
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
  for (let i = 0; i < 300; i++) {
    if (done()) return
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
  throw new Error(`timed out waiting for ${what}`)
}
