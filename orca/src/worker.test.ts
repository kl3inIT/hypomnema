// Runs src/worker.ts's activate(orca) over a mock Orca host, with the control plane faked behind fetch.
import type { JsonValue } from 'orca'
import { afterEach, assert, mockHost, mountWorker, test } from 'orca/testing'
import { fakeControlPlane, until } from './fake-control-plane'
import { board, question, run, task } from './fixtures'
import type { InboxState } from './protocol'
import * as main from './worker'

const connected = { settings: { server: 'http://cp.test' }, secrets: { token: 'tok' } }

let restore = () => {}
afterEach(() => restore())

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
  const one = await mountWorker(main, { host: first })
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
  const two = await mountWorker(main, { host: second })
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
  const worker = await mountWorker(main, { host })
  await until(() => lastState(worker.posts)?.status === 'live', 'the stream open')
  assert.deepEqual(cp.opened, ['7'])
  assert.equal(lastState(worker.posts)?.items.length, 0)
  assert.equal(host.callsOf('notifications.show').length, 0)

  cp.board = board(
    [task('t1', 'run_1', { status: 'blocked', blocked_reason: 'needs_auth' })],
    [run('run_1')],
    8
  )
  cp.emit(8)
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

  const worker = await mountWorker(main, { host: mockHost(connected) })
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
      message: 'question q1 already has another answer'
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
  const worker = await mountWorker(main, { host })
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
