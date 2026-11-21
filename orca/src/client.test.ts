// What the client throws, which reaches the panel and the log.
import { assert, test } from 'orca/testing'
import { ControlPlane } from './client'

// Scenario: fetch refuses a token a header cannot carry and repeats it in its error; the client says the error
// without the token.
test('a refused header does not repeat the token', async () => {
  const cp = new ControlPlane('http://127.0.0.1:59999', 'canary-7f3a\nsecond-line')
  const message = await cp.board().then(
    () => 'answered',
    (e: unknown) => (e instanceof Error ? e.message : String(e))
  )

  assert.match(message, /\[token\]|invalid/i)
  assert.equal(message.includes('canary'), false)
})
