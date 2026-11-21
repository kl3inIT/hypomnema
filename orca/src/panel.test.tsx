// What the owner sees and presses in the panel, with the worker's answers stubbed.
import { assert, mockHost, mount, test } from 'orca/testing'
import { board, question, run, task } from './fixtures'
import * as panel from './panel'
import type { InboxState } from './protocol'
import { waitingFor } from './waiting'

const stalled = run('run_1', {
  review_stall: {
    task_id: 'review',
    pull_request: 'https://github.com/kl3inIT/hypomnema/pull/840',
    verdict: 'incorrect',
    waiting_for_owner: true
  }
})
const asking = task('t1', 'run_1')
const live: InboxState = {
  server: 'https://cp.test',
  hasToken: true,
  status: 'live',
  items: waitingFor(board([asking, task('review', 'run_1', { status: 'succeeded' })], [stalled]), [
    question('q1', asking)
  ])
}

// Scenario: before a token is kept the panel asks for the server and the token, and sends them to the worker once.
test('asks for the server and a token, then connects', async () => {
  const sent: unknown[] = []
  const host = mockHost({
    requests: {
      'inbox.get': () => ({ server: '', hasToken: false, status: 'unconfigured', items: [] }),
      'inbox.connect': (input) => (sent.push(input), { ...live, items: [] })
    }
  })
  const view = await mount(panel, { host })
  assert.ok(view.find({ text: 'Connect to Hypomnema' }))

  await view.type({ label: 'Server' }, 'https://cp.test')
  await view.type({ label: 'Owner token' }, 'tok')
  await view.press({ text: 'Connect' })
  assert.deepEqual(sent, [{ server: 'https://cp.test', token: 'tok' }])
  assert.ok(view.find({ text: 'Nothing waits for you' }))
  assert.deepEqual(view.errors, [])
  await view.unmount()
})

// Scenario: a question's options are buttons; one that someone else answered first says so.
test('answers with the option pressed and says when it was already answered', async () => {
  const sent: unknown[] = []
  const host = mockHost({
    requests: {
      'inbox.get': () => live,
      'inbox.answer': (input) => (
        sent.push(input),
        { outcome: 'taken', message: 'question q1 already has another answer' }
      )
    }
  })
  const view = await mount(panel, { host })
  assert.equal(view.find({ role: 'status' })?.text, '2')

  await view.press({ text: 'Behind a login' })
  assert.deepEqual(sent, [{ questionId: 'q1', option: 1 }])
  assert.ok(view.find({ text: 'Already answered: question q1 already has another answer' }))

  await view.type({ label: 'Details' }, 'SSO only')
  await view.press({ text: 'Other' })
  assert.deepEqual(sent[1], { questionId: 'q1', option: 2, text: 'SSO only' })
  assert.deepEqual(view.errors, [])
  await view.unmount()
})

// Scenario: Open shows the stalled review's pull request, and the Task on the web for anything else.
test('opens the pull request of a stalled review, else the Task on the web', async () => {
  const host = mockHost({ requests: { 'inbox.get': () => live } })
  const view = await mount(panel, { host })

  const opens = view.findAll({ text: 'Open' })
  assert.equal(opens.length, 2)
  await view.press(opens[0]!)
  await view.press(opens[1]!)
  assert.deepEqual(host.callsOf('shell.openExternal'), [
    { url: 'https://cp.test/team?task=t1' },
    { url: 'https://github.com/kl3inIT/hypomnema/pull/840' }
  ])
  assert.deepEqual(view.refusals, [])
  await view.unmount()
})

// Scenario: the Task was canceled before the answer reached the server: the panel says so, not "Already answered".
test('says a question was canceled when the answer came too late', async () => {
  const host = mockHost({
    requests: {
      'inbox.get': () => live,
      'inbox.answer': () => ({
        outcome: 'canceled',
        message: 'conflict: question q1 was canceled (task_canceled)'
      })
    }
  })
  const view = await mount(panel, { host })

  await view.press({ text: 'Public' })
  assert.ok(view.find({ text: 'Canceled: conflict: question q1 was canceled (task_canceled)' }))
  assert.equal(view.find({ text: /Already answered/ }), null)
  await view.unmount()
})
