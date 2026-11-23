import type { Routine, RoutineRun } from "@/api/generated/types.gen"
import { since } from "@/lib/time"

// What the Inbox's Scan now says about the triage Routine (issue #1115), apart from how it draws it.

// triageName is the name of the Routine that reads the owner's new messages into the Inbox, as deploy/thu-ky/setup.sh
// sets it on the main Bot.
export const triageName = "triage"

// scanning says a run of the triage goes, or waits for the owner, so the server would refuse a second one (POST
// /routines/{id}/runs answers a conflict).
export const scanning = (triage: Pick<Routine, "last_run">) =>
  triage.last_run?.state === "running" || triage.last_run?.state === "waiting"

// readBack says the Routine as read shows the run begun, or a later one: until then the run is going, whatever the
// read before it said.
export const readBack = (
  triage: Pick<Routine, "last_run">,
  begun: Pick<RoutineRun, "started_at">,
) =>
  !!triage.last_run &&
  new Date(triage.last_run.started_at).getTime() >= new Date(begun.started_at).getTime()

// lastRead is the line beside the button: when the triage last read the new messages, or that its last read failed.
export function lastRead(triage: Pick<Routine, "last_run">, busy: boolean, now = Date.now()) {
  const run = triage.last_run
  if (busy || scanning(triage)) return "Reading new messages…"
  if (!run) return "Hasn't read new messages yet"

  const when = since(run.ended_at ?? run.started_at, now)

  return run.state === "succeeded"
    ? `Read new messages ${when}`
    : `Couldn't read new messages ${when}`
}
