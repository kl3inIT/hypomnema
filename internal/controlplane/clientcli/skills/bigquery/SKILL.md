---
name: bigquery
description: >-
  Reads app-usage figures from the GA4 export in BigQuery with Hypomnema's bigquery_* tools: installs, active users,
  time in app, sessions, uninstalls, with one SELECT checked and dry-run before it runs. Use when the owner asks for
  a figure or a report from Google Analytics data kept in BigQuery. Not for writing to BigQuery, and not for
  dashboards (Superset has those).
metadata:
  hypomnema:
    requires:
      integration: bigquery
---

# BigQuery through Hypomnema

Read the GA4 export in BigQuery with Hypomnema's `bigquery_*` tools. The control plane holds the service account's
key, gets its token and runs every call: you never see a key or a token, and you never sign in. The account's IAM
roles decide what it may read: Data Viewer on the GA4 dataset, Job User on the billing project.

## Which account

- You have the tools only when you were given a BigQuery account: a worker the ones its Run was given, a Bot the
  ones the owner switched on for chats and allowed for it. Without one there are no `bigquery_*` tools; say so, and
  do not look for another way.
- With one account, every tool uses it. With several, each tool takes `account`, the account's label.
- A call for an account you were not given is refused with "this agent was not given bigquery:<label>".

## The tools

| Tool | What it returns |
|---|---|
| `bigquery_datasets` | The datasets of the GA4 export's project. Free. |
| `bigquery_tables` (`dataset` may be left out) | A dataset's tables with their type; without `dataset`, the account's GA4 dataset. Free. |
| `bigquery_table` (`table`) | A table's columns, nested ones too (`event_params.key`), its rows, bytes and partitioning. Free. |
| `bigquery_query` (`sql`, `dry_run`, `max_rows`) | `columns`, `rows` (each row's values in the order of `columns`), `total_rows`, `truncated`, `bytes_processed`, `bytes_billed`, `computed_at`. With `dry_run: true`: only what the query would process; nothing is billed. |

Look first: `bigquery_tables`, then `bigquery_table` on the newest `events_YYYYMMDD`, then the query.

## A query

- **One SELECT** (or `WITH … SELECT`), over the native GA4 tables. No script, no statements separated by `;`, no
  `CREATE`, `DECLARE`, `SET`, `EXPORT` or `CALL`, no temporary or user functions, no `ML.`, `AI.` or
  `VECTOR_SEARCH`, no `EXTERNAL_QUERY`, no `#legacySQL`. Such a query is refused before it is sent.
- **Dry-run first** with `dry_run: true`: it says the bytes the query would process, free. Then run it.
- **The bytes cap.** Each account has a cap on the bytes one query may bill (10 GiB unless the owner set another).
  A query whose dry run says more is refused before it runs. The cap counts BigQuery's own bytes only.
- **Read few bytes.** BigQuery bills the columns read, whatever the rows: name only the columns needed, never
  `SELECT *`; `LIMIT` does not lower the bytes billed. Keep the days a constant range:
  `` FROM `project.dataset.events_*` WHERE _TABLE_SUFFIX BETWEEN '20261101' AND '20261119' ``. At least 10 MB is
  billed for each table read, and a wildcard over several tables is not cached.
- A query runs for at most 30 seconds as a tool. At the limit its cancel is requested: it may still finish and be
  billed up to the cap. Narrow the query rather than running it again.
- `max_rows` is 50 unless you ask for more, up to 10,000. Aggregate in SQL (counts, sums, per day) rather than
  reading rows to count them yourself. When `truncated` is true, more rows exist than you got: say so.

## GA4 export

- One table a day, `events_YYYYMMDD`, and `events_intraday_YYYYMMDD` for today, often a day late. `events_*` matches
  the intraday tables too, whose `_TABLE_SUFFIX` starts with `intraday_`; a `BETWEEN '2026…' AND '2026…'` range
  leaves them out.
- One row an event: `event_name`, `event_date` (`YYYYMMDD` text), `event_timestamp` (microseconds),
  `user_pseudo_id` (the app instance), `platform`, `device.*`, `geo.*`, `app_info.*`, and `event_params`, a list of
  `key` and `value.string_value` / `value.int_value` / `value.double_value`:
  `(SELECT value.int_value FROM UNNEST(event_params) WHERE key = 'engagement_time_msec')`.
- Installs are `first_open`; uninstalls `app_remove` (Android only); sessions `session_start`; time in app the sum
  of `engagement_time_msec` of `user_engagement`. Active users of a day are
  `COUNT(DISTINCT user_pseudo_id)` of that day's events.
- **The figures are as of the call**: `computed_at` says when the job ended. Say it, and say which days the
  query covered; the newest day may be incomplete.

## Personal data

`user_pseudo_id`, `user_id`, device and location fields describe people. Report counts and summaries; do not paste
ids or rows about one person into a chat, a document or a message unless the owner asked for exactly that. Never
print, ask for or pass on a key, a token or a password.

## What runs at once, and what waits for the owner

Reading and querying run at once, unless the owner set them to ask or to never for the account. The tool's answer
says what happened:

| The tool says | What it means, and what to do |
|---|---|
| Its normal answer | It ran. Report what came back, with `computed_at`. |
| `Refused: …` | The query is not one SELECT, its dry run refused it, or it is over the cap. Nothing was billed. Do not work around it: fix the query as the message says, or say why it cannot be answered. |
| `Waiting for the owner's approval (apr_…)` | He did not answer in time; nothing was run. Do what the rest of the message says. |
| `Rejected by the owner`, `Refused: the owner set … to never` | Do not run it again or another way. Say so. |
| `… What the account may read is its IAM roles` | The account may not read that; nothing was run. Say what is missing: the data owner grants it at Google. |
| `the query was stopped … cancel requested` | It ran past its time. Narrow the query. |

## In a Routine's script

A script has commands, not tools: `hypomnema bigquery query --sql-file <file>` prints the same answer as JSON, or
`--format csv` the rows with a header line; `--dry-run`, `--max-rows N` and `--timeout D` (up to `10m`) are as
above, and `hypomnema bigquery tables <project.dataset>` lists the tables. When the owner set queries to ask, the
command waits up to 30 minutes for his approval before the query runs, and the Routine's script is stopped at its
`timeout_seconds` (600 by default): set it to at least 1,920 plus the query's `--timeout` in seconds, **2520 for
`--timeout 10m`**, or the script is stopped while it waits. A read-only Routine has no script: its prompt uses the
tools.
