// The items and their keys; that the rule is the web's is web/src/features/work/orca-waiting.test.ts.
import { assert, test } from 'orca/testing'
import { board, question, run, task } from './fixtures'
import { waitingFor } from './waiting'

// Scenario: one item per Task that waits for the owner, keyed so a notification is sent once per question, and
// once per failure, stall or block of a Task.
test('each item is keyed by its question, else by its Task and status', () => {
  const asking = task('t1', 'run_1', { spec: 'Add a login page\nwith SSO' })
  const failed = task('t2', 'run_1', { status: 'failed' })
  const gate = task('t3', 'run_1', { status: 'blocked', blocked_reason: 'gate' })
  const review = task('t4', 'run_2', { status: 'succeeded' })
  const stalled = run('run_2', {
    review_stall: {
      task_id: 't4',
      pull_request: 'javascript:alert(1)',
      verdict: 'incorrect',
      waiting_for_owner: true
    }
  })
  const items = waitingFor(board([asking, failed, gate, review], [run('run_1'), stalled]), [
    question('q1', asking),
    question('q2', gate, { mode: 'gate', addressed_to: 'coordinator' })
  ])

  assert.deepEqual(
    items.map((i) => [i.key, i.kind, i.task, i.text]),
    [
      ['q1', 'question', 'Add a login page', 'Public or behind a login?'],
      ['t2:failed', 'failed', 'spec of t2', 'The Task failed'],
      ['t3:blocked', 'blocked', 'spec of t3', 'The Task is blocked: it waits for you'],
      ['t4:succeeded', 'review', 'spec of t4', 'The review stopped at its last round: incorrect']
    ]
  )
  // A link a worker wrote is kept only when it is http(s).
  assert.equal(items[3]?.link, undefined)
  assert.equal('link' in (items[3] ?? {}), false)
})
