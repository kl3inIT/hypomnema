# Search Console

> Read this skill's guide first: which account a command acts as, and the rule for anything that writes.

`gws` has no Search Console. Call its REST API with `curl`, run through Hypomnema so that it goes through the same
proxy as `gws`: the proxy adds the account's credential and applies the owner's rules. Send no token and no
`Authorization` header yourself.

```bash
hypomnema tool run google [--account <label>] -- curl -sS <url> [curl's own flags]
```

`--account` goes before `--`, as `gws --account` does; leave it out when you were given one Google account. Only
Google's hosts go through: any other address is refused. The account must have been granted Search Console at
sign-in, and must be a user of the site's property in Search Console.

## The site

A site is named as Search Console names its property: `sc-domain:example.com` for a domain property, or
`https://example.com/` (with its last slash) for a URL-prefix one. In a URL path it is percent-encoded:
`sc-domain%3Aexample.com`, `https%3A%2F%2Fexample.com%2F`. A sitemap's address in a path is encoded the same way.
List the properties first: the account's `permissionLevel` on each says whether it can read it
(`siteUnverifiedUser` cannot).

## Calls

The host is `https://searchconsole.googleapis.com`. Two path prefixes share it: `/webmasters/v3` for sites,
sitemaps and search performance, `/v1` for URL Inspection; the wrong one answers 404.

| What | Request | Runs |
|---|---|---|
| The properties | `GET /webmasters/v3/sites` | at once |
| One property | `GET /webmasters/v3/sites/{site}` | at once |
| The sitemaps: when Google last read each, errors, warnings | `GET /webmasters/v3/sites/{site}/sitemaps` | at once |
| One sitemap | `GET /webmasters/v3/sites/{site}/sitemaps/{sitemap}` | at once |
| Search performance | `POST /webmasters/v3/sites/{site}/searchAnalytics/query` | at once |
| Whether a page is on Google, and why not | `POST /v1/urlInspection/index:inspect` | at once |
| Submit or remove a sitemap | `PUT` or `DELETE /webmasters/v3/sites/{site}/sitemaps/{sitemap}` | waits for the owner |
| Add or remove a property | `PUT` or `DELETE /webmasters/v3/sites/{site}` | waits for the owner |

```bash
# The properties
hypomnema tool run google -- curl -sS https://searchconsole.googleapis.com/webmasters/v3/sites

# Why a page is or is not on Google: coverage, last crawl, the sitemaps and pages that refer to it, the canonical
hypomnema tool run google -- curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"inspectionUrl": "https://example.com/page", "siteUrl": "sc-domain:example.com"}' \
  https://searchconsole.googleapis.com/v1/urlInspection/index:inspect

# The top queries of two weeks
hypomnema tool run google -- curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"startDate": "2026-11-01", "endDate": "2026-11-14", "dimensions": ["query"], "rowLimit": 100}' \
  'https://searchconsole.googleapis.com/webmasters/v3/sites/sc-domain%3Aexample.com/searchAnalytics/query'
```

In a search performance query, data lags about two days, so today is empty. Leave `dimensions` out for the site's
total: rows under Google's privacy threshold are withheld, so a sum over a dimension is less than the total.

## What it cannot do

- **Request indexing**: Google offers it only in the Search Console web page. Say which URLs need it; the owner
  presses the button.
- Removals, Page experience and the Pages report as a whole have no API. URL Inspection answers for one page at a
  time, at most 2,000 a day for a property.

What the proxy refuses or holds is said on stderr after curl ends, as for `gws`; curl itself only sees its proxy
answer 502 or 403.
