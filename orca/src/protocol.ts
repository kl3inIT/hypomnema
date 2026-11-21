// The panel <-> worker contract, declared once: src/panel.tsx and src/worker.ts both read it.
import type { Item } from './waiting'

// What the panel shows: where the control plane is, whether a token is kept, how the stream goes, and the items.
export type InboxState = {
  server: string
  hasToken: boolean
  status: 'unconfigured' | 'connecting' | 'live' | 'error'
  error?: string
  items: Item[]
}

// How an answer went: sent, refused because the question was answered or canceled meanwhile, or not sent.
export type AnswerResult =
  | { outcome: 'answered' }
  | { outcome: 'taken' | 'canceled' | 'failed'; message: string }

declare module 'orca' {
  interface PluginRequests {
    'inbox.get': { output: InboxState }
    /** Keeps the server in settings and a new token in secrets, then follows that control plane. */
    'inbox.connect': { input: { server: string; token?: string }; output: InboxState }
    'inbox.answer': {
      input: { questionId: string; option?: number; text?: string }
      output: AnswerResult
    }
  }
  interface PluginPanelEvents {
    /** Posted to every open panel when the items or the stream's status change. */
    'inbox.changed': InboxState
  }
}
