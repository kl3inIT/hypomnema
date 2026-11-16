# Search Console (v1)

> Read this skill's guide first: how a command is run here, which account it acts as, and the rule for anything that writes.

Search Console is not one of `gws`'s named services, so it is called by its API and version, `searchconsole:v1`
(gws: "Use '<api>:<version>' syntax for unlisted APIs"). The account must have been granted Search Console at
sign-in, and must be a user of the site's property in Search Console.

```bash
gws searchconsole:v1 <resource> [sub-resource] <method> [flags]
```

A site is named as Search Console names its property: `sc-domain:example.com` for a domain property, or
`https://example.com/` (with its last slash) for a URL-prefix one. `sites list` shows which the account has.

## API Resources

| Command | What it does | Runs |
|---|---|---|
| `sites list` | The properties the account can see, with its permission on each | at once |
| `sites get --params '{"siteUrl": SITE}'` | One property | at once |
| `sitemaps list --params '{"siteUrl": SITE}'` | The sitemaps submitted, when Google last read each, errors and warnings | at once |
| `sitemaps get --params '{"siteUrl": SITE, "feedpath": URL}'` | One sitemap | at once |
| `searchanalytics query --params '{"siteUrl": SITE}' --json BODY` | Clicks, impressions, CTR and position, by query, page, country, device or date | at once |
| `urlInspection index inspect --json '{"inspectionUrl": URL, "siteUrl": SITE}'` | Whether a page is on Google and why not: coverage, last crawl, the sitemaps and pages that refer to it, the canonical Google chose | at once |
| `sitemaps submit --params '{"siteUrl": SITE, "feedpath": URL}'` | Submit a sitemap | waits for the owner |
| `sitemaps delete`, `sites add`, `sites delete` | Remove a sitemap; add or remove a property | waits for the owner |

A `searchanalytics query` body: `{"startDate": "2026-11-01", "endDate": "2026-11-14", "dimensions": ["query"],
"rowLimit": 100}`. Data lags about two days, so today is empty. Leave `dimensions` out for the site's total: rows
under Google's privacy threshold are withheld, so a sum over a dimension is less than the total.

## What it cannot do

- **Request indexing**: Google offers it only in the Search Console web page. Say which URLs need it; the owner
  presses the button.
- Removals, Page experience and the Pages report as a whole have no API. URL Inspection answers for one page at a
  time, at most 2,000 a day for a property.

Run `gws searchconsole:v1 --help` for the resources, and `gws schema searchconsole:v1.<resource>.<method>` before an
unfamiliar call.
