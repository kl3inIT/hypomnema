# BigQuery as an Integration

A BigQuery Connection reads the GA4 export of an app in BigQuery (issue #976): installs, active users, time in app,
sessions, uninstalls, for the reports Bots build. One Connection is one service account's JSON key, given to
Hypomnema once, with the GA4 dataset it reads and the project its queries are billed to. The control plane signs
the account's JWT, keeps the access token in memory and runs every call; the key and the token never reach a worker,
a node or a Bot. Only the owner's recorded read of a vault secret returns the key
([decision](../../docs/decisions/2026-11-10-owner-reads-secret-values.md)).

What the Connection may read is what the account's IAM roles grant: Hypomnema does not filter datasets. So the
account is made for this alone, with the least it needs. Each step on Google Cloud below is done by the data owner,
the person who administers the GA4 export's project. Replace `DATA_PROJECT`, `DATASET` (such as
`analytics_123456789`) and `BILLING_PROJECT` (the project queries are billed to; often `DATA_PROJECT`).

## 1. A service account for Hypomnema alone

```sh
gcloud iam service-accounts create hypomnema-ga4 --project BILLING_PROJECT \
  --display-name "Hypomnema GA4 reader"
```

Its address is `hypomnema-ga4@BILLING_PROJECT.iam.gserviceaccount.com`. Do not reuse an account that has other
roles: whatever it can read, a query can read.

## 2. Data Viewer on the GA4 dataset only

Grant BigQuery Data Viewer on the export dataset, not on the project. In the console: BigQuery, the dataset,
Sharing, Permissions, Add principal, the account's address, role BigQuery Data Viewer. Or with `bq`:

```sh
bq show --format=prettyjson DATA_PROJECT:DATASET > dataset.json
# add to "access": {"role": "READER", "userByEmail": "hypomnema-ga4@BILLING_PROJECT.iam.gserviceaccount.com"}
bq update --source dataset.json DATA_PROJECT:DATASET
```

## 3. Job User on the billing project

Queries run as jobs in the billing project, which needs BigQuery Job User and nothing more:

```sh
gcloud projects add-iam-policy-binding BILLING_PROJECT \
  --member serviceAccount:hypomnema-ga4@BILLING_PROJECT.iam.gserviceaccount.com \
  --role roles/bigquery.jobUser
```

Do not grant project-level BigQuery Data Viewer, BigQuery User or Admin, Connection User or Admin, or Cloud Run
Invoker. Keep the account away from datasets that hold remote functions, remote models, or BigLake or external
tables: their reads are billed outside the cap below. Connecting refuses a dataset that holds an external table.

## 4. A new key

```sh
gcloud iam service-accounts keys create hypomnema-ga4-key.json \
  --iam-account hypomnema-ga4@BILLING_PROJECT.iam.gserviceaccount.com
```

Make a new key for Hypomnema; never use a key that was sent in a chat or a group, and send this one to the owner
by a private way. The owner deletes the file once it is connected.

## 5. In Hypomnema

On the web: Integrations, BigQuery, Add key; choose the key file and give the dataset. Or from a terminal:

```sh
hypomnema integrations connect bigquery --label ga4 --key-file hypomnema-ga4-key.json \
  --dataset DATA_PROJECT.DATASET --billing-project BILLING_PROJECT [--location US] [--max-bytes 10GiB]
```

Connecting checks the key step by step, each free, and keeps nothing when one fails: Google gives a token for
it; each dataset's tables are listed (one holding an external table is refused); the newest native
`events_YYYYMMDD` table is read once (its schema and one row, which is dropped); a dry run of a `SELECT` on it runs
in the billing project. `hypomnema integrations check bigquery:ga4` runs the same steps again.

Then switch it on for chats and give it to a Bot, as any Connection (`hypomnema integrations chats bigquery:ga4
on`). Try it:

```sh
hypomnema bigquery tables DATA_PROJECT.DATASET
hypomnema bigquery query --dry-run --sql-file sample.sql
hypomnema bigquery query --sql-file sample.sql --format csv
```

## The cost cap

Each query is one GoogleSQL `SELECT`, checked by a lexer and by a dry run before it runs, under a cap on the bytes
one query job may bill on demand: 10 GiB unless set (about $0.061 at $6.25 a TiB). A query whose dry run says more
is refused before it runs; the job itself carries the same cap. Change it with
`hypomnema integrations max-bytes bigquery:ga4 2GiB` or on the web (Bytes cap). It is not a daily budget: use
BigQuery's custom daily quotas on the billing project for that. A job stopped at its time limit is cancelled, which
BigQuery may not honour before it ends and bills.

## In a Routine's script

When the owner sets `bigquery.query` to ask, a Routine's run waits up to 30 minutes for his approval before the
query runs, and its script is stopped at the Routine's `timeout_seconds` (600 by default). Give such a Routine at
least 1,920 seconds plus the query's `--timeout`: 2,520 for `--timeout 10m`
(`hypomnema routines set <name> --timeout 2520`).

## Disconnecting

`hypomnema integrations disconnect bigquery:ga4` removes the Connection and its key from the vault. The key still
exists at Google: the data owner deletes it in the Cloud console (IAM, Service accounts, the account, Keys) or with
`gcloud iam service-accounts keys delete KEY_ID --iam-account hypomnema-ga4@BILLING_PROJECT.iam.gserviceaccount.com`.
