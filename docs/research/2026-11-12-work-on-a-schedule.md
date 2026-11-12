# Work on a schedule: connectors that sync and bots that run routines (2026-11-12)

The owner asked on 2026-11-12 for the figures of a Superset dashboard to reach Lark by themselves, through
Hypomnema. That needs something to run on a schedule, which `docs/vision.md` names and nothing builds yet. This
note compares how five references run work on a schedule. The design that followed is in issue #211; the reading
of Superset is issue #203.

Revisions read: Onyx `da837e6`, MemoryOS `9c206cbb`, OpenMausBot `1a4e4da`, Hermes Agent `e3bf4be`, OpenClaw
`7b3c6ed`.

What was read: each reference by a sub-agent, in its code and documents, with the paths below. Read directly, in
addition: Hermes `cron/AGENTS.md` (the job's fields, the script rule) and `tools/approval.py` L704-716; OpenClaw
`docs/automation/cron-jobs/payloads.md` (command payloads), `how-it-works.md` L77-104 (a job an agent makes),
`docs/tools/exec-approvals.md` L609-623 (standing grants), `src/cron/service/auto-disable.ts` L14. **Not read
directly:** everything else in the table; a path names where the sub-agent read it.

## Two kinds of scheduled work

| Kind | Who has it | What runs |
|---|---|---|
| A connector that syncs | Onyx, MemoryOS | Code of the product: read a source, write the product's own store, with run history, retries and checkpoints |
| A routine of a bot | OpenMausBot, Hermes, OpenClaw | A turn of the bot on a prompt. Hermes and OpenClaw also run a script or a command with no model |

No reference has both in one mechanism. Hermes (`no_agent`) and OpenClaw (`command`, `script` payloads) are the
ones where a scheduled job can be plain code a person or an agent supplied.

## The comparison

| Point | Onyx | MemoryOS | OpenMausBot | Hermes | OpenClaw |
|---|---|---|---|---|---|
| The record | `refresh_freq` seconds on the connector; no stored next run (`backend/onyx/db/models.py` L2099-2140) | `source_sync_state`: interval minutes, paused, `next_sync_at` (`V134__connector_standardization.sql` L231-243) | `Routine` in one JSON file with its runs (`server/routines.ts` L51-86, L328) | A JSON file per profile (`cron/jobs.py` L95, L1899-1930) | SQLite: jobs and run history (`src/cron/types.ts` L404) |
| Schedule | Interval, at least 60 s | Interval minutes; "no cron … is exposed" (`docs/specs/connector.md` L130) | Once, daily, interval (5 to 1440 min), 5-field cron with a required IANA zone, on a limit's reset (`server/routine-schedules.ts` L120-150) | Duration, "every" phrase, 5-field cron with a zone, ISO one-shot (`cron/AGENTS.md`) | `at`, `every`, 5- or 6-field cron with a zone (`docs/automation/cron-jobs/schedules.md`) |
| Tick | Celery beat, every 15 s (`beat_schedule.py` L100-102) | db-scheduler, every minute (`ControlPlaneConfiguration.java` L113-116) | A timer in the process, 60 s (`server/due-timer.ts`) | A thread in the gateway, 60 s (`cron/scheduler_thread.py`) | In the gateway process |
| Overdue after downtime | Due at the next tick | Runs once; next = now + interval (`JdbcSourceSyncRepository.java` L237-241) | One run if at most 12 h late, else `missed` (`server/routines.ts` L280, L1036-1046) | One run within half the period, 2 min to 2 h; `next_run_at` is advanced before the run (`cron/AGENTS.md`) | Rescheduled, not replayed at once (`how-it-works.md` L26) |
| One run at a time | A lock, the row lock and a check for a live attempt (`db/indexing_coordination.py` L38-90) | A partial unique index on live attempts and a lease (`V23__add_durable_google_drive_sync.sql` L81-82) | Per routine `skip` or `queue` (`server/routines.ts` L1041-1060) | A running job is skipped (`cron/scheduler_tick.py` L178) | Not due while queued or running (`src/cron/service/jobs-scheduling.ts` L280-287) |
| A run's record | `IndexAttempt`: seven states, counts, heartbeat (`db/enums.py` L47-56) | `source_sync_attempts`: state, phase, attempts, error; started, trigger, duration on the page (`docs/specs/connector.md` L35) | `RoutineRun`: state, output, error, cost; 2,000 kept (`server/routines.ts` L94-141, L284) | A file per run and an executions ledger (`cron/jobs.py` L3353-3361) | Run history, 7 days, 2,000 rows a job (`managing-jobs.md`) |
| Retry | Backs off on the failed streak (`docprocessing/utils.py` L186-274) | 30 s to 1 h; the sixth failure fails the run (`docs/specs/connector.md` L188-196) | One retry (`server/routine-recovery.ts`) | Only when the model is unreachable: 5, 15, 30 min (`cron/unreachable_retry.py` L33) | Recurring: 30 s, 60 s, 5 m, 15 m, 60 m, reset by a success (`managing-jobs.md` L290) |
| Repeated failure | Flag and notice; paused on Cloud only (`docprocessing/tasks.py` L1290-1319) | Error code on the source | Notice to the owner | Incidents (`cron/incidents.py`) | Alert after 2 in a row (`delivery.md` L185-200); switched off after 10 (`auto-disable.ts` L14) |
| A refused credential | The pair becomes `INVALID` (`run_docfetching.py` L1095-1146) | The run fails at once; the credential needs reconnecting; the source stays (`docs/specs/connector.md` L193) | No retry | | A permanent error disables a one-shot |
| Run now, pause | Both; run now goes through the same locks (`server/documents/connector.py` L2509-2543) | Schedule, pause, sync now (`docs/specs/connector.md` L128-182) | Yes | `hermes cron run|pause|resume` | Yes |
| Who makes one | Admin | Admin | Person or bot; a bot's applies at once (`server/routine-requests.ts` L1014) | Person or agent (`cronjob` tool) | Person, or agent after the person confirms a sentence that restates it, then one test run (`how-it-works.md` L87-101) |
| A script | | | None; `checks` are code before a turn (`docs/proactive-routines.md` L66-75) | A file under the profile's `scripts/`, never a command line; `no_agent` makes it the whole job | `command` and `script` payloads; only an operator admin makes a command job |
| An approval with nobody present | | | The card waits; unanswered, the run stops (`docs/routine-schedules.md` L93-100) | `approvals.cron_mode` | The card waits on approval surfaces; "Always allow" is a standing grant scoped to the job |
| A scheduled run making schedules | | | | Refused by default (`cron/scheduler.py` L419-430) | An isolated run may remove only its own job |
| Limits | | | 5 to 1440 min; a run limit in minutes | Idle 600 s; a script 3600 s | Agent turn 60 min, command 10 min, script 5 min (`how-it-works.md` L25) |

## What they share

- The scheduler is in the product's own process and ticks about once a minute.
- A time missed while the process was down is never replayed one by one: at most one run.
- One run of a job at a time, and "run now" takes the same path as the schedule.
- Each run leaves a record a person can read, and repeated failure reaches the owner.

## Where they differ

- Connectors schedule by an interval only; routines also take cron with a time zone.
- A bot's routine applies at once (OpenMausBot) or after the person confirms and sees a test run (OpenClaw).
- Unattended approvals: the run waits (OpenMausBot, OpenClaw) or follows a setting (Hermes); only OpenClaw has a
  grant that lasts for the job.

## What followed

The owner chose on 2026-11-12 not to build a connector that syncs: moving data between services is ETL, not this
product. Hypomnema builds a Routine of a Bot, a prompt or a script, after Hermes and OpenClaw; the sync is a script
a Bot writes. The design, with the reference of each part, is in issue #211.

## Sources

- Onyx: `.tmp/onyx/backend/onyx` (`db/models.py`, `background/celery/tasks/`, `server/documents/`).
- MemoryOS: `D:\MemoryOS` (`docs/specs/connector.md`, `memoryos-core/src/main/resources/db/migration/`,
  `memoryos-core/src/main/java/io/memoryos/connector/sync/`); its failure rules cite Onyx.
- OpenMausBot: `.tmp/openmausbot` (`server/routines.ts`, `server/routine-schedules.ts`, `docs/routine-schedules.md`,
  `docs/runtime-contract.md`).
- Hermes Agent: `.tmp/hermes-agent` (`cron/AGENTS.md`, `cron/jobs.py`, `cron/scheduler.py`, `tools/approval.py`).
- OpenClaw: `.tmp/openclaw` (`docs/automation/cron-jobs/`, `docs/tools/exec-approvals.md`, `src/cron/`).
