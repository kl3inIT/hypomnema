import type { Question, Run, TaskBoard, TaskDetail } from "@/api/generated/types.gen"

// Test data shaped as the client API returns it.

export const run = (id: string, m: Partial<Run> = {}): Run => ({
  id,
  objective: `objective of ${id}`,
  status: "open",
  created_by: "pi",
  repo: "kl3inIT/hypomnema",
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
  ...m,
})

export const task = (id: string, runId: string, m: Partial<TaskDetail> = {}): TaskDetail => ({
  id,
  run_id: runId,
  position: 1,
  profile: "implementer",
  spec: `spec of ${id}`,
  status: "running",
  failure_count: 0,
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
  ...m,
})

export const question = (id: string, t: TaskDetail, m: Partial<Question> = {}): Question => ({
  id,
  dispatch_id: "dsp_1",
  task_id: t.id,
  run_id: t.run_id,
  mode: "wait",
  addressed_to: "owner",
  text: "Public or behind a login?",
  options: [
    { label: "Public" },
    { label: "Behind a login" },
    { label: "Other", allows_text: true },
  ],
  status: "open",
  created_at: "2026-11-09T10:05:00Z",
  ...m,
})

export const board = (tasks: TaskDetail[], runs: Run[]): TaskBoard => ({
  data: tasks,
  runs,
  after: 7,
})
