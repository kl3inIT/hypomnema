// `orca plugin check` renders the panel once per scenario, each with the worker on a fresh host.
import { defineCheck } from 'orca/testing'

export default defineCheck({
  // No server or token yet: the panel asks for them.
  fresh: {},
  // A server is set and a token kept, but nothing answers there: the panel says it is connecting, then the error.
  unreachable: { settings: { server: 'http://127.0.0.1:59999' }, secrets: { token: 'tok' } }
})
