// Test data shaped as the client API returns it, as the web's src/features/work/fixtures.ts makes it.
import type { Board, Question, Run, Task } from './waiting'

export const run = (id: string, m: Partial<Run> = {}): Run => ({
  id,
  objective: `objective of ${id}`,
  status: 'open',
  ...m
})

export const task = (id: string, runId: string, m: Partial<Task> = {}): Task => ({
  id,
  run_id: runId,
  spec: `spec of ${id}`,
  status: 'running',
  ...m
})

export const question = (id: string, t: Task, m: Partial<Question> = {}): Question => ({
  id,
  task_id: t.id,
  run_id: t.run_id,
  mode: 'wait',
  addressed_to: 'owner',
  text: 'Public or behind a login?',
  options: [
    { label: 'Public' },
    { label: 'Behind a login' },
    { label: 'Other', allows_text: true }
  ],
  status: 'open',
  ...m
})

export const board = (tasks: Task[], runs: Run[], after = 7): Board => ({
  data: tasks,
  runs,
  after
})
