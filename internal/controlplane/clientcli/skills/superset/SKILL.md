---
name: superset
description: >-
  Reads the figures of the owner's Superset dashboards with Hypomnema's superset_* tools: finds a dashboard by its
  title, lists its charts and filters, and returns a chart's rows as the dashboard shows them, as JSON or CSV, with
  the SQL Superset ran. Can also send any other request to Superset's API. Use when the owner asks for a number, a
  table or a list that is on a Superset dashboard, or to copy a chart's data somewhere. Not for drawing charts and
  not for pictures of a dashboard.
metadata:
  hypomnema:
    requires:
      integration: superset
---

# Superset through Hypomnema

Read a Superset dashboard's charts as data with Hypomnema's `superset_*` tools. The control plane signs in to
Superset with the account the owner connected and adds its token to each request: you never see a password or a
token, and you never log in.

The tools have the shape of Preset's `sup` (a dashboard list, a chart's data) and of treg's `treg call` (one
request relayed as it is). They were written for Hypomnema and tried against Superset 6.1.0.

## Which account

- You have the tools only when you were given a Superset account: a worker the ones its Run was given, a Bot the
  ones the owner switched on for chats and allowed for it. Without one there are no `superset_*` tools; say so,
  and do not look for another way.
- With one account, every tool uses it. With several, each tool takes `account`, the account's label: name one on
  each call.
- A call for an account you were not given is refused with "this agent was not given superset:<label>".

## The tools

| Tool | What it returns |
|---|---|
| `superset_dashboards` (`search` may be left out) | One JSON object: `data`, each dashboard's `id`, `title`, `slug` and `changed_at`. At most a hundred, by title. |
| `superset_dashboard` (`dashboard`) | The dashboard's `title`; its `charts`, each one's `id`, `name`, `kind` (`table`, `pie`, …), `dataset` and `read`; its `filters`, each one's `name`, `kind`, `column` and `default`, the value it has when the dashboard opens. `dashboard` is its number or its slug. |
| `superset_chart_data` (`dashboard`, `chart`, `filters`) | One JSON object: `columns` (the names), `rows` (each row's values in the order of `columns`), `row_count`, `sql` (the query Superset ran), `computed_at`, `cached`, `source`. With `format: "csv"`: the rows alone, with a header line. `filters` sets the dashboard's filters, below. |
| `superset_filter_values` (`dashboard`, `filter`, `search`) | One JSON object: `data`, each `value` a select filter offers with its `count` of rows, the most frequent first, at most 1000. |
| `superset_api` (`method`, `path`, `body`) | Superset's answer to one request: `status`, `content_type`, `body` (base64 when `base64` is true) and the `action` it was. An answer that is not a success is the tool's error. |

Find the numbers first: `superset_dashboards` with a `search`, then `superset_dashboard`, then
`superset_chart_data`.

## What a chart's data is

- The rows are the ones the chart shows **when its dashboard opens**: the chart's own filters, order and row limit,
  and the dashboard's filters at the values they start with, but those you set with `filters`.

## A filtered question: set the dashboard's filters

For "last week", "batch imports only", "unit X", set the filter as someone would in the dashboard's filter bar:
`filters` on `superset_chart_data`, `{"Unit": ["Sales", "Support"], "Time": ["Last week"]}`, each filter named as
`superset_dashboard` lists it. Superset then computes the chart's figures with it.

- A select filter takes the values `superset_filter_values` lists; a time filter one range (`Last week`,
  `2026-11-01 : 2026-11-08`); an empty list clears a filter. A filter not named keeps its default.
- A filter applies only to the charts it lists in `charts`; another chart refuses it, naming the filters it has.
- **Never answer a filtered question by filtering, summing or averaging the rows of an unfiltered read.** A count
  of distinct people, a median or a rate is right only when Superset computes it with the filter.
- A Routine's script does the same with `hypomnema superset chart <dashboard> <chart> --filter "Unit=Sales"`,
  a flag a value, and lists values with `hypomnema superset filter-values <dashboard> <filter>`.
- **The figures are as of the call.** `computed_at` says when: the time Superset cached them when `cached` is
  true, the time of your call otherwise. Say that time when you report a figure; do not present it as live.
- **The chart's row limit holds.** A list longer than the chart's limit is cut as it is on the dashboard. When
  `row_count` equals a round limit (1000, 5000, 10000), say the list may be cut.
- `source` says how the query was come by: `saved` is the query Superset saved with the chart; `built` is one built
  from the chart's settings, for the kinds `table`, `big_number_total`, `pie`, `echarts_timeseries_line` and
  `echarts_timeseries_bar`.
- A line or bar chart over time comes back as one row for each point of each series (the x value, the series'
  names, the metrics), not as one column a series.
- A chart of another kind that has no saved query answers **"not read yet: <kind>"**. Do not guess its figures
  from another chart: say which chart could not be read and why. The owner can open the chart in Superset and save
  it, which saves its query, or ask for the kind to be added.

## Lists of people are personal data

A chart may list people: names, user names, emails, what each did. What you read then is personal data.

- Read such a chart only when the owner's request needs it.
- Do not paste the list into a chat, a document or a message unless the owner asked for exactly that, and then only
  where he said.
- Prefer the count or the summary when that answers the question.

## What runs at once, and what waits for the owner

Every request to Superset is matched to an action, and the owner set an answer for each action of each account; you
do not see the list.

- **Reading** (every tool above but some `superset_api` calls) runs at once, unless he set it otherwise.
- **Creating, changing or deleting** something in Superset, **running SQL** (`/api/v1/sqllab/execute/`) and
  **administering users and roles** are held for the owner's approval, unless he set them otherwise. He is shown
  the request you sent. The account's own roles at Superset still decide: a read-only account is refused a write
  there with a 403.
- **What he set to never** is refused.

Make the call you mean. Do not ask the owner first in words: the approval he is shown is the question. What the
tool answers tells you what happened:

| The tool says | What it means, and what to do |
|---|---|
| Its normal answer | It ran. Report what came back. |
| `Waiting for the owner's approval (apr_…): … was NOT run` | He did not answer in time; nothing was done. Do what the rest of the message says: in a chat, tell him in one line what waits and end your turn, and you are woken when he answers; as a worker, ask him with your `ask` tool and wait. |
| You are told it was **approved** | Make the same call again, with the same arguments and the same body. It passes once. A changed call is a new request and asks again. |
| `Rejected by the owner`, or you are told it was **rejected** | Do not make it again, and do not look for another way to do the same thing. Say so in your answer. |
| `Refused: the owner set … to never` | The same: say so, do not work around it. |
| `Refused: <account> cannot "…" at <integration>:<label>: <reason>` | The account itself cannot do it at the service; nothing was sent. Do not try it another way. Say what is missing in your answer: the owner grants it there. |
| `Refused: a request's path …` | The path was not a plain path of Superset's API. Write it as `/api/v1/...` with no `..`, no encoded slash and no host. |

A request to read or summarize is not a request to change anything: do not start a write the owner did not ask
for.

## What the account cannot do

The control plane reads the account's roles at Superset and knows, part by part, what it cannot do: a read-only
role reads dashboards, charts and datasets, and is refused SQL Lab, queries, logs, users and roles, row level
security, annotations and every change. Where you run `hypomnema superset dashboards`, its listing ends with what
the account cannot do; read it before the first other call. With the tools or the commands, a call to one of them
is refused before it reaches Superset, with `Refused: … cannot …` and the role that lacks the permission. Do not try it another way: say what is missing, and the
owner gives the account a role at Superset.

## Any other request: `superset_api`

For what has no tool of its own: a dataset's columns and metrics, the list of charts, a saved query.

| `method` | `path` | `body` |
|---|---|---|
| `GET` | `/api/v1/dataset/27` | |
| `GET` | `/api/v1/chart/?q={"page_size":20,"filters":[{"col":"slice_name","opr":"ct","value":"sales"}]}` | |
| `POST` | `/api/v1/chart/data` | the query, a JSON object |

- The path starts with `/api/v1/` and may carry a query. Superset's lists take their question in `q`, as JSON or in
  its own short form (`q=(page_size:20)`).
- The body is a JSON object.
- Superset describes its whole API at `/api/v1/_openapi`: read it with `GET /api/v1/_openapi` when you need a
  method's parameters. It is long; read it once, for the method you need.

## Never

- Never print a token, a password or a credential, and never write one into a file, a commit or a message.
- Never send a chart's rows outside the task you were given.
