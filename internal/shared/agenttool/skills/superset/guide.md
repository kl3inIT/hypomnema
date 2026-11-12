---
name: superset
description: >-
  Reads the figures of the owner's Superset dashboards with Hypomnema's own superset commands: finds a dashboard by
  its title, lists its charts and filters, and returns a chart's rows as the dashboard shows them, as JSON or CSV,
  with the SQL Superset ran. Can also send any other request to Superset's API. Use when the owner asks for a
  number, a table or a list that is on a Superset dashboard, or to copy a chart's data somewhere. Not for drawing
  charts and not for pictures of a dashboard.
---

# Superset through Hypomnema

Read a Superset dashboard's charts as data with the `superset` commands of the `hypomnema` binary. The control plane signs in to
Superset with the account the owner connected and adds its token to each request: you never see a password or a
token, and you never log in.

The commands have the shape of Preset's `sup` (a dashboard list, a chart's data) and of treg's `treg call` (one
request relayed as it is). They were written for Hypomnema and tried against Superset 6.1.0.

## Which account

- When one Superset account is connected, every command uses it. Nothing to add.
- When several are, name one on each command with `--connection <label>`. A command that names none is refused
  with the list of labels.
- You can use only an account the owner switched on for chats. A command for another is refused with "this agent
  was not given superset:<label>"; say so, and do not look for another way.

## The commands

```bash
hypomnema superset dashboards --search <text>
hypomnema superset dashboard <dashboard>
hypomnema superset chart <dashboard> <chart>
hypomnema superset chart <dashboard> <chart> --format csv
hypomnema superset chart <dashboard> <chart> --sql
hypomnema superset api <METHOD> <path>
hypomnema superset api <METHOD> <path> --data <file>
```

`--search` may be left out. Every command but `superset chart`, whose answer is JSON already, takes `--json`.

| Command | What it returns |
|---|---|
| `superset dashboards` | One line a dashboard: its number and its title. At most a hundred, by title. |
| `superset dashboard <dashboard>` | The dashboard's title; each chart's number, name, kind (`table`, `pie`, …) and dataset; each filter's name, kind, column and the value it has when the dashboard opens. `<dashboard>` is its number or its slug. |
| `superset chart <dashboard> <chart>` | One JSON object: `columns` (the names), `rows` (each row's values in the order of `columns`), `row_count`, `sql` (the query Superset ran), `computed_at`, `cached`, `source`. With `--format csv`: the rows alone, with a header line. With `--sql`: the query alone. |
| `superset api` | Superset's answer to one request, printed as it came. Fails with the status when Superset's answer is not a success. |

Find the numbers first: `superset dashboards --search`, then `superset dashboard <number>`, then
`superset chart <dashboard> <chart>`.

## What a chart's data is

- The rows are the ones the chart shows **when its dashboard opens**: the chart's own filters, order and row limit,
  and the dashboard's filters at the values they start with. A value someone picked in a filter in their browser
  is not applied.
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

- **Reading** (every command above but some `superset api` calls) runs at once, unless he set it otherwise.
- **Creating, changing or deleting** something in Superset, **running SQL** (`/api/v1/sqllab/execute/`) and
  **administering users and roles** are held for the owner's approval, unless he set them otherwise. He is shown
  the request you sent. The account's own roles at Superset still decide: a read-only account is refused a write
  there with a 403.
- **What he set to never** is refused.

Run the command you mean. Do not ask the owner first in words: the approval he is shown is the question. What the
command answers tells you what happened:

| The command says | What it means, and what to do |
|---|---|
| Its normal answer | It ran. Report what came back. |
| `Waiting for the owner's approval (apr_…): … was NOT run` | He did not answer in time; nothing was done. Tell him in one line what waits for his approval, and end your turn; you are woken when he answers. |
| You are told it was **approved** | Run the same command again, with the same arguments and the same body. It passes once. A changed command is a new request and asks again. |
| `Rejected by the owner`, or you are told it was **rejected** | Do not run it, and do not look for another way to do the same thing. Say so in your answer. |
| `Refused: the owner set … to never` | The same: say so, do not work around it. |
| `Refused: a request's path …` | The path was not a plain path of Superset's API. Write it as `/api/v1/...` with no `..`, no encoded slash and no host. |

A request to read or summarize is not a request to change anything: do not start a write the owner did not ask
for.

## Any other request: `superset api`

For what has no command of its own: a dataset's columns and metrics, the list of charts, a saved query.

```bash
hypomnema superset api GET /api/v1/dataset/27
hypomnema superset api GET '/api/v1/chart/?q={"page_size":20,"filters":[{"col":"slice_name","opr":"ct","value":"sales"}]}'
hypomnema superset api POST /api/v1/chart/data --data query.json
```

- The path starts with `/api/v1/` and may carry a query. Superset's lists take their question in `q`, as JSON or in
  its own short form (`q=(page_size:20)`). Put the path in single quotes.
- A body is JSON, read from a file or from standard input (`--data -`). It is never an argument.
- The answer is printed as Superset sent it. `--json` prints it inside one object with its `status`,
  `content_type` and the `action` the request was.
- Superset describes its whole API at `/api/v1/_openapi`: read it with `superset api GET /api/v1/_openapi` when
  you need a method's parameters.

## Never

- Never print a token, a password or a credential, and never write one into a file, a commit or a message.
- Never send a chart's rows outside the task you were given.
