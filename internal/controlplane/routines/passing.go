package routines

import "regexp"

// What makes a failure a passing one: a rate limit, an overloaded or failing
// server, the network, a timeout. Only such a failure is tried again earlier
// than the schedule's next time. The patterns are OpenClaw's
// (src/cron/retry-hint.ts L20-52, MIT), unchanged but for Go's syntax: a
// number counts only with the words that make it a status, so that "exited
// with 503 lines" is no server error.
var passing = []*regexp.Regexp{
	// rate_limit
	regexp.MustCompile(`(?i)\b(?:https?(?:/\d(?:\.\d)?)?|status(?:[ _-]?code)?|response(?:[ _-]?code)?|` +
		`http(?:[ _-]?status)?)\b[\s:=#"'(]{0,6}429\b|\b(?:provider\s+)?api[ _-]?error\b[\s:=#"'(]{0,6}429\b|` +
		`\b(?:requested\s+)?url\s+returned\s+error\b[\s:=#"'(]{0,6}429\b|\b429\b[\s:)\].,-]*(?:rate[_ -]?limit` +
		`(?:ed|ing)?(?:[_ -](?:error|exceeded|reached))?|too many requests|resource has been exhausted|` +
		`quota(?:\s+(?:exceeded|exhausted|depleted|reached))?)\b|\brate[_ -]?limit(?:ed|ing)?` +
		`(?:[_ -](?:error|exceeded|reached))?\b|\btoo many requests\b|\bresource has been exhausted\b|` +
		`\btokens per day\b|^\s*429\s*$`),
	// overloaded
	regexp.MustCompile(`(?i)^\s*529(?:\s*$|[\s:)\].,-]*(?:api\b.*\bbusy\b|(?:please\s+)?try\s+again\b))|` +
		`\b(?:https?(?:/\d(?:\.\d)?)?|status(?:[ _-]?code)?|response(?:[ _-]?code)?|http(?:[ _-]?status)?|` +
		`(?:provider\s+)?api[ _-]?error|(?:requested\s+)?url\s+returned\s+error)\b[\s:=#"'(]{0,6}529\b|` +
		`\boverloaded(?:_error)?\b|high demand|temporar(?:ily|y) overloaded|capacity exceeded`),
	// network
	regexp.MustCompile(`(?i)(network|fetch failed|socket|econnreset|econnrefused|eai_again|enetdown|ehostunreach|` +
		`ehostdown|enetreset|enetunreach|epipe)`),
	// timeout
	regexp.MustCompile(`(?i)(timeout|timed out|stalled before execution start|etimedout)`),
	// server_error
	regexp.MustCompile(`(?i)\b(?:https?|status(?:[ _]code)?|response(?:[ _]code)?|http(?:[ _]status)?)\b` +
		`[\s:=#"']{0,4}5\d{2}\b|\b5\d{2}\b[\s:)\].,-]*(?:internal server error|server error|bad gateway|` +
		`service unavailable|gateway time-?out)\b|\binternal server error\b|\bbad gateway\b|` +
		`\bservice unavailable\b|\bgateway time-?out\b|\b5xx\b|^\s*5\d{2}\s*$`),
}

// passes reports whether an error's text says a failure that passes by
// itself.
func passes(text string) bool {
	for _, p := range passing {
		if p.MatchString(text) {
			return true
		}
	}

	return false
}
