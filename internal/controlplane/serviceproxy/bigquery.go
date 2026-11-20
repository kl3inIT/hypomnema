package serviceproxy

// BigQueryID is the BigQuery Integration's id (integration.BigQueryID).
const BigQueryID = "bigquery"

// bigqueryCatalog is what an agent does with a BigQuery Connection (issue #976): acts the control plane runs itself
// with the service account's token (act.go), reaching no host through the tunnel. Both read: listing and describing
// is free, and a query is one SELECT that passed the lexical gate and its dry run under the bytes cap, so both run
// on their own until the owner sets ask or never (design v3, decision 19; Superset's skill asks before SQL,
// SKILL.md:91, where SQL may write). A query the gate refuses is refused, never asked.
func bigqueryCatalog() Catalog {
	return Catalog{Integration: BigQueryID, Actions: []Action{
		{
			ID: "bigquery.read", Service: "BigQuery", Name: "Read datasets and tables", Kind: Reads,
			Text: "List the datasets and tables and read a table's schema; nothing is billed",
		},
		{
			ID: "bigquery.query", Service: "BigQuery", Name: "Run a query", Kind: Reads,
			Text: "One SELECT over the GA4 tables, dry-run first, billed on demand up to the Connection's bytes cap",
		},
	}}
}
