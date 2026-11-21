// Runs src/worker.ts's activate(orca) over a mock Orca host, with the control plane faked behind fetch.
import type { JsonValue } from 'orca'
import { afterEach, assert, mockHost, mountWorker, test } from 'orca/testing'
import { fakeControlPlane, gate, until } from './fake-control-plane'
import { board, question, run, task } from './fixtures'
import type { InboxState } from './protocol'
import * as main from './worker'

const connected = { settings: { server: 'http://cp.test' }, secrets: { token: 'tok' } }

let restore = () => {}
// Every worker a test mounts, stopped after it even when an assertion failed, so none keeps following.
const mounted: { deactivate(): Promise<void> }[] = []
afterEach(async () => {
  for (const w of mounted.splice(0)) await w.deactivate()
  restore()
})

async function mount(host: ReturnType<typeof mockHost>) {
  const worker = await mountWorker(main, { host })
  mounted.push(worker)

  return worker
}

function lastState(posts: readonly { event: string; payload?: unknown }[]): InboxState | undefined {
  return posts.filter((p) => p.event === 'inbox.changed').at(-1)?.payload as InboxState | undefined
}

// Scenario: the owner is told once of each new item, even after Orca restarts the worker; an item that left is
// forgotten, so it is told again if it comes back.
test('notifies each new item once, across a restart', async () => {
  const failed = task('t1', 'run_1', { status: 'failed' })
  const asking = task('t2', 'run_1')
  const cp = fakeControlPlane(board([failed, asking], [run('run_1')]), [question('q1', asking)])
  restore = cp.restore

  const first = mockHost(connected)
  const one = await mount(first)
  await until(() => first.callsOf('notifications.show').length === 2, 'two notifications')
  await until(() => Array.isArray(first.data().storage.notified), 'the keys kept')
  assert.deepEqual(first.data().storage.notified, ['t1:failed', 'q1'])
  assert.deepEqual(first.callsOf('statusBar.update').at(-1), {
    id: 'waiting',
    text: '2',
    tone: 'warning',
    visible: true
  })
  await one.deactivate()

  // Orca starts the worker again: the same items, the question answered meanwhile, a new failure.
  const later = task('t3', 'run_1', { status: 'failed' })
  cp.board = board([failed, asking, later], [run('run_1')])
  cp.questions = []
  const second = mockHost({
    ...connected,
    storage: first.data().storage as Record<string, JsonValue>
  })
  const two = await mount(second)
  await until(
    () =>
      JSON.stringify(second.data().storage.notified) === JSON.stringify(['t1:failed', 't3:failed']),
    'the keys of the items there are now'
  )
  assert.deepEqual(second.callsOf('notifications.show'), [
    { title: 'A Task failed', body: 'The Task failed\nobjective of run_1 · spec of t3' }
  ])
  assert.deepEqual(one.refusals, [])
  assert.deepEqual(two.refusals, [])
  await two.deactivate()
})

// Scenario: an event on the stream makes the worker read the board again; the stream starts at the snapshot's
// cursor, so nothing between them is missed.
test('an event refreshes the items from the snapshot cursor', async () => {
  const t = task('t1', 'run_1')
  const cp = fakeControlPlane(board([t], [run('run_1')], 7))
  restore = cp.restore

  const host = mockHost(connected)
  const worker = await mount(host)
  await until(() => lastState(worker.posts)?.status === 'live', 'the stream open')
  assert.deepEqual(cp.opened, ['7'])
  assert.equal(lastState(worker.posts)?.items.length, 0)
  assert.equal(host.callsOf('notifications.show').length, 0)

  cp.board = board(
    [task('t1', 'run_1', { status: 'blocked', blocked_reason: 'needs_auth' })],
    [run('run_1')]
  )
  cp.emit()
  await until(() => lastState(worker.posts)?.items.length === 1, 'the blocked Task listed')
  assert.equal(
    lastState(worker.posts)?.items[0]?.text,
    'The Task is blocked: its account needs signing in again'
  )
  await worker.deactivate()
})

// Scenario: the owner picks an option in the panel; the worker posts that option, and says when someone answered
// first.
test('answering posts the chosen option', async () => {
  const t = task('t1', 'run_1')
  const cp = fakeControlPlane(board([t], [run('run_1')]), [question('q1', t)])
  restore = cp.restore

  const worker = await mount(mockHost(connected))
  await until(() => lastState(worker.posts)?.status === 'live', 'the stream open')

  assert.deepEqual(await worker.request('inbox.answer', { questionId: 'q1', option: 1 }), {
    outcome: 'answered'
  })
  assert.deepEqual(cp.answers, [{ id: 'q1', body: { option: 1 }, auth: 'Bearer tok' }])

  cp.answerStatus = 409
  assert.deepEqual(
    await worker.request('inbox.answer', { questionId: 'q1', option: 2, text: 'Both' }),
    {
      outcome: 'taken',
      message: 'conflict: question q1 already has another answer'
    }
  )
  assert.deepEqual(cp.answers[1]?.body, { option: 2, text: 'Both' })
  await worker.deactivate()
})

// Scenario: the owner pastes the server and a token once; the token goes to Orca's secrets and is never sent back.
test('connecting keeps the server in settings and the token in secrets', async () => {
  const cp = fakeControlPlane(board([], [run('run_1')]))
  restore = cp.restore

  const host = mockHost()
  const worker = await mount(host)
  assert.equal((await worker.request('inbox.get')).status, 'unconfigured')

  const state = await worker.request('inbox.connect', { server: 'http://cp.test/', token: ' tok ' })
  assert.equal(state.server, 'http://cp.test')
  assert.equal(state.hasToken, true)
  assert.equal(JSON.stringify(state).includes('tok"'), false)
  assert.equal(host.data().secrets.token, 'tok')
  assert.equal(host.data().settings.server, 'http://cp.test')
  await until(() => lastState(worker.posts)?.status === 'live', 'the stream open')
  await worker.deactivate()
})

// Scenario: a question opens while the snapshot is read, after the questions were read and before the server
// reads the board's cursor. The cursor is taken before both reads, so the stream brings its event and the worker
// reads the questions again.
test('a question opened while the snapshot is read is not missed', async () => {
  const t = task('t1', 'run_1')
  const cp = fakeControlPlane(board([t], [run('run_1')], 10))
  restore = cp.restore
  const held = gate()
  cp.holdBoard = held.wait

  const host = mockHost(connected)
  const worker = await mount(host)
  await until(() => cp.reads.includes('/tasks'), 'the board asked for')
  await new Promise((resolve) => setTimeout(resolve, 20))

  // The question opens (event 11) while GET /tasks has not reached the server yet.
  cp.questions = [question('q1', t)]
  cp.emit()
  cp.holdBoard = undefined
  held.open()

  await until(() => host.callsOf('notifications.show').length === 1, 'the question notified')
  assert.deepEqual(
    lastState(worker.posts)?.items.map((i) => i.key),
    ['q1']
  )
  await worker.deactivate()
})

// Scenario: the read after an event fails (the server was busy); the worker reads again, rather than showing
// what it read before until another event comes.
test('a refresh that fails is tried again', async () => {
  const t = task('t1', 'run_1')
  const cp = fakeControlPlane(board([t], [run('run_1')]))
  restore = cp.restore

  const host = mockHost(connected)
  const worker = await mount(host)
  await until(() => lastState(worker.posts)?.status === 'live', 'the stream open')

  cp.board = board([task('t1', 'run_1', { status: 'failed' })], [run('run_1')])
  cp.failBoard = 1
  cp.emit()

  await until(() => lastState(worker.posts)?.items.length === 1, 'the failed Task listed')
  assert.equal(lastState(worker.posts)?.status, 'live')
  assert.equal(host.callsOf('notifications.show').length, 1)
  await worker.deactivate()
})

// Scenario: Orca stops the worker while the second of three notifications is being shown. The key of each one
// shown is kept as it is shown, the one shown during the stop too; the stopped worker shows no more, and the
// worker started again shows only the third.
test('a restart between two notifications repeats none', async () => {
  const cp = fakeControlPlane(
    board(
      ['t1', 't2', 't3'].map((id) => task(id, 'run_1', { status: 'failed' })),
      [run('run_1')]
    )
  )
  restore = cp.restore
  const held = gate()
  let shown = 0
  const first = mockHost({
    ...connected,
    results: {
      'notifications.show': async () => {
        shown += 1
        if (shown === 2) await held.wait

        return { delivered: true }
      }
    }
  })
  const one = await mount(first)
  try {
    await until(
      () => first.callsOf('notifications.show').length === 2,
      'the second notification shown'
    )
    await until(
      () => JSON.stringify(first.data().storage.notified) === JSON.stringify(['t1:failed']),
      'the first key kept as it was shown'
    )
  } catch (e) {
    // The test kit's deactivate waits for every host call: let the held one end, so a failure does not hang.
    held.open()
    throw e
  }

  const stopping = one.deactivate()
  held.open()
  await stopping
  assert.deepEqual(first.data().storage.notified, ['t1:failed', 't2:failed'])
  assert.equal(first.callsOf('notifications.show').length, 2)

  const second = mockHost({
    ...connected,
    storage: first.data().storage as Record<string, JsonValue>
  })
  await mount(second)
  await until(
    () =>
      JSON.stringify(second.data().storage.notified) ===
      JSON.stringify(['t1:failed', 't2:failed', 't3:failed']),
    'every key kept'
  )
  assert.deepEqual(second.callsOf('notifications.show'), [
    { title: 'A Task failed', body: 'The Task failed\nobjective of run_1 · spec of t3' }
  ])
})

// Scenario: the Task was canceled between the panel showing its question and the owner answering: the server
// refuses the answer as a conflict, and the worker says the question was canceled, not answered.
test('an answer to a canceled question says it was canceled', async () => {
  const t = task('t1', 'run_1')
  const cp = fakeControlPlane(board([t], [run('run_1')]), [question('q1', t)])
  restore = cp.restore

  const worker = await mount(mockHost(connected))
  await until(() => lastState(worker.posts)?.status === 'live', 'the stream open')

  cp.questions = [question('q1', t, { status: 'canceled' })]
  cp.answerStatus = 409
  assert.deepEqual(await worker.request('inbox.answer', { questionId: 'q1', option: 0 }), {
    outcome: 'canceled',
    message: 'conflict: question q1 was canceled (task_canceled)'
  })
  await worker.deactivate()
})

// Scenario: a token that a header cannot carry (a newline pasted with it) is refused without being repeated, and
// one kept already never shows in what the panel or the log says.
test('a token never shows in an error or the log', async () => {
  const cp = fakeControlPlane(board([], [run('run_1')]))
  restore = cp.restore
  const canary = 'canary-7f3a\nsecond-line'

  const fresh = await mount(mockHost())
  const refused = await fresh
    .request('inbox.connect', { server: 'http://cp.test', token: canary })
    .then(
      () => undefined,
      (e: unknown) => (e instanceof Error ? e.message : String(e))
    )
  assert.ok(refused, 'the token is refused')
  assert.equal(refused.includes('canary'), false)

  const kept = await mountWorker(main, {
    host: mockHost({ settings: { server: 'http://cp.test' }, secrets: { token: canary } })
  })
  await until(() => lastState(kept.posts)?.status === 'error', 'the error shown')
  assert.equal(JSON.stringify(kept.posts).includes('canary'), false)
  assert.equal(kept.logs.join('\n').includes('canary'), false)
})
