package modelgateway

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Why an account rests, in one closed set for every adapter (issue #1305): the references keep such a set beside the
// provider's own message, and never fold a used-up plan into a rate limit (Bifrost core/schemas/failureclass.go
// FailureClassQuota and FailureClassRateLimit; OmniRoute open-sse/config/constants.ts RateLimitReason; omp
// packages/ai/src/error/rate-limit.ts RateLimitReason; opencode packages/ai/src/provider-error.ts QuotaExceededError,
// RateLimitError and AuthenticationError).

// CoolingCode is why an account rests.
type CoolingCode string

// The codes, in the words the account's card and the API use.
const (
	// CoolingUsageLimit is a plan's window or allowance used up: a 5-hour, weekly or monthly limit.
	CoolingUsageLimit CoolingCode = "usage_limit"
	// CoolingRateLimit is a short throttle.
	CoolingRateLimit CoolingCode = "rate_limit"
	// CoolingBilling is credits, a balance or a budget spent (402).
	CoolingBilling CoolingCode = "billing"
	// CoolingAuth is the key or the sign-in refused (401, 403).
	CoolingAuth CoolingCode = "auth"
	// CoolingOverloaded is the provider out of capacity (5xx, 529).
	CoolingOverloaded CoolingCode = "overloaded"
	// CoolingUnknown is a failure that says none of the above.
	CoolingUnknown CoolingCode = "unknown"
)

// CoolingCodes are every code, for the stores and the API that keep them.
func CoolingCodes() []CoolingCode {
	return []CoolingCode{
		CoolingUsageLimit, CoolingRateLimit, CoolingBilling, CoolingAuth, CoolingOverloaded, CoolingUnknown,
	}
}

// Rest is until when an account, or one model of it, rests, why (Code) and the provider's own words for it
// (Reason); zero when it does not.
type Rest struct {
	Until  time.Time
	Code   CoolingCode
	Reason string
}

// The codes a provider names its failures by, lowercased, as opencode reads them (provider-error.ts:92-115): what
// says a balance or budget is spent, what says a plan's allowance is used up, a refused key, and the provider's own
// failure. opencode keeps the first two in one QUOTA_CODES; the usage limits are told apart here (row 1 of #1305), with
// Codex's usage_limit_reached (CLIProxyAPI, codex.Failure.UsageLimit).
var (
	billingCodes = map[string]bool{
		"insufficient_quota": true, "usage_not_included": true, "billing_error": true,
		"budgetlimiterror": true, "creditlimitexceeded": true,
		// Google's credits balance spent, a billing cap omp reads from ErrorInfo's reason
		// (packages/ai/src/error/rate-limit.ts:212-215, isBillingCapUsageLimit :508; test
		// rate-limit-utils.test.ts:357-363).
		"insufficient_g1_credits_balance": true,
	}
	usageCodes = map[string]bool{
		"gousagelimiterror": true, "freeusagelimiterror": true, "usage_limit_reached": true,
		// Google's quota used up (omp rate-limit.ts:210-211, isUsageLimitOutcome :277).
		"quota_exhausted": true,
	}
	authCodes = map[string]bool{
		"authentication_error": true, "permission_error": true, "permission_denied": true, "api_key_invalid": true,
	}
	serverCodes = map[string]bool{
		"api_error": true, "internal_error": true, "internalserverexception": true,
		"modelstreamerrorexception": true, "overloaded_error": true, "server_error": true,
		"server_is_overloaded": true, "slow_down": true, "serviceunavailableexception": true,
	}
)

// The words of a failure, matched only where opencode matches them (provider-error.ts:146-158): a spent balance or a
// used-up plan only on a 429, where throttles and account caps share a status (QUOTA_TEXT, split in two here), and a
// rate limit or the provider's own failure on any status (RATE_LIMIT_TEXT, SERVER_ERROR_TEXT).
var (
	billingText = regexp.MustCompile(`(?i)insufficient[-_\s]?(?:quota|balance)|budget exceeded`)
	// usageText is QUOTA_TEXT's usage limit, quota exceeded and limit exhausted. RE2 has no look-behind: what
	// QUOTA_TEXT leaves out with one (Cerebras's token_quota_exceeded, a rate limit exhausted) is told apart by
	// usageWords.
	usageText     = regexp.MustCompile(`(?i)(token_)?quota[-_\s]?exceeded|(rate[-_\s]?)?limit exhausted|usage limit`)
	rateLimitText = regexp.MustCompile(`(?i)rate increased too quickly|rate[-_\s]?limit|too[_\s]?many[_\s]?requests`)
	serverText    = regexp.MustCompile(`(?i)\b(?:try again|(?:please |you can )?retry (?:the |this |your )?request|` +
		`try (?:the |this |your )?request again|(?:currently |temporarily )?at capacity|overloaded|` +
		`temporarily unavailable|service[-_\s]?unavailable|(?:server|internal)[-_\s]?error|server (?:is )?busy|` +
		`provider returned (?:an )?error|resource[-_\s]?exhausted|upstream (?:connect|connection|request))\b`)
)

// hasCode reports whether any of codes, lowercased, is in set.
func hasCode(codes []string, set map[string]bool) bool {
	for _, c := range codes {
		if set[strings.ToLower(strings.TrimSpace(c))] {
			return true
		}
	}

	return false
}

// usageWords reports whether text says a plan's allowance is used up (usageText), less the matches QUOTA_TEXT's
// look-behinds leave out.
func usageWords(text string) bool {
	for _, m := range usageText.FindAllStringSubmatch(text, -1) {
		if m[1] == "" && m[2] == "" {
			return true
		}
	}

	return false
}

// classifyFailure says why a provider's refusal rests an account, the one reading every adapter uses (row 2 of
// #1305), in the order the issue approved: the provider's structured code first, then the status, then the words.
// A 402 is always billing, and a spent balance's or a used-up plan's words are trusted only on a 429. The code sets
// and the words are opencode's (provider-error.ts:92-158). Two places follow opencode's grouping
// (provider-error.ts:249-283) inside that order: a generic throttle code (rate_limit_error, rate_limit_exceeded,
// too_many_requests) is read after a 429's words, because Anthropic- and OpenAI-shaped endpoints put it on every 429,
// a used-up plan's included (claude.ParseFailure, typeOf); and a server code decides only when no 4xx contradicts it,
// as Google's RESOURCE_EXHAUSTED rides on a 429. status is zero for a failure that carries none (a stream's event).
func classifyFailure(status int, codes []string, text string) CoolingCode {
	lowered := make([]string, 0, len(codes))
	for _, c := range codes {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			lowered = append(lowered, c)
		}
	}

	has := func(set map[string]bool) bool {
		for _, c := range lowered {
			if set[c] {
				return true
			}
		}

		return false
	}

	contains := func(parts ...string) bool {
		for _, c := range lowered {
			for _, p := range parts {
				if strings.Contains(c, p) {
					return true
				}
			}
		}

		return false
	}

	clientScoped := status >= http.StatusBadRequest && status < http.StatusInternalServerError
	throttleCode := contains("rate_limit") || has(map[string]bool{"too_many_requests": true, "throttlingexception": true})

	switch {
	case status == http.StatusPaymentRequired:
		return CoolingBilling
	// The provider's structured code.
	case has(billingCodes):
		return CoolingBilling
	case has(usageCodes):
		return CoolingUsageLimit
	case has(authCodes):
		return CoolingAuth
	case !clientScoped && (has(serverCodes) || contains("exhausted", "unavailable")):
		return CoolingOverloaded
	// The status.
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return CoolingAuth
	case status == http.StatusTooManyRequests && billingText.MatchString(text):
		return CoolingBilling
	case status == http.StatusTooManyRequests && usageWords(text):
		return CoolingUsageLimit
	case status == http.StatusTooManyRequests || throttleCode:
		return CoolingRateLimit
	case status >= http.StatusInternalServerError:
		return CoolingOverloaded
	// The words, when no status decides.
	case rateLimitText.MatchString(text):
		return CoolingRateLimit
	case status < http.StatusBadRequest && serverText.MatchString(text):
		return CoolingOverloaded
	default:
		return CoolingUnknown
	}
}

// providerCodes are the codes an error body carries where opencode reads them (utils/provider-codes.ts
// providerCodes: code, error_type, error.code, error.type, error.error_type, error.status), and the `[code]` label
// OpenCode keeps at the start of a message whose upstream code it rewrote (provider-error.ts GATEWAY_CODE_LABEL).
func providerCodes(body []byte) []string {
	var codes []string

	var answer struct {
		Code      any `json:"code"`
		ErrorType any `json:"error_type"`
		Error     any `json:"error"`
		Message   any `json:"message"`
	}

	if json.Unmarshal(body, &answer) != nil {
		if m := zenCodeLabel.FindStringSubmatch(string(body)); m != nil {
			codes = append(codes, m[1])
		}

		return codes
	}

	str := func(v any) {
		if text, ok := v.(string); ok {
			codes = append(codes, text)
		}
	}

	label := func(v any) {
		if text, ok := v.(string); ok {
			if m := zenCodeLabel.FindStringSubmatch(text); m != nil {
				codes = append(codes, m[1])
			}
		}
	}

	str(answer.Code)
	str(answer.ErrorType)
	label(answer.Message)

	if inner, ok := answer.Error.(map[string]any); ok {
		str(inner["code"])
		str(inner["type"])
		str(inner["error_type"])
		str(inner["status"])
		label(inner["message"])
	}

	return codes
}

// providerMessage is the provider's own words for a refusal, kept beside its code (row 5 of #1305): the message of the
// common error bodies in opencode's order (provider-error.ts providerErrorMessage: error.message, error, message,
// detail), else the body as it came.
func providerMessage(body []byte) string {
	var answer struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}

	if json.Unmarshal(body, &answer) == nil {
		var inner struct {
			Message string `json:"message"`
		}

		var text string

		switch {
		case json.Unmarshal(answer.Error, &inner) == nil && strings.TrimSpace(inner.Message) != "":
			return strings.TrimSpace(inner.Message)
		case json.Unmarshal(answer.Error, &text) == nil && strings.TrimSpace(text) != "":
			return strings.TrimSpace(text)
		case strings.TrimSpace(answer.Message) != "":
			return strings.TrimSpace(answer.Message)
		case strings.TrimSpace(answer.Detail) != "":
			return strings.TrimSpace(answer.Detail)
		}

		// A JSON string is the message itself.
		var whole string
		if json.Unmarshal(body, &whole) == nil {
			return strings.TrimSpace(whole)
		}
	}

	return strings.TrimSpace(string(body))
}

// resetsIn finds a reset the provider names in its words: "Resets in 2hr 15min", "resets in 3 days", "will reset in
// 71h48m47s" (omp packages/utils/src/fetch-retry.ts:13-22 QUOTA_RESET_PATTERN, WILL_RESET_IN_PATTERN and
// RESET_IN_HR_MIN_PATTERN).
var resetsIn = regexp.MustCompile(`(?i)resets?\s+in\s+~?`)

// resetPair is one number and its unit after "resets in"; the pairs that follow one another are summed.
var resetPair = regexp.MustCompile(`^[\s,]*(?:and\s+)?(\d+(?:\.\d+)?)\s*([a-zA-Z]+)`)

// resetUnits are the units omp reads (fetch-retry.ts:266-291 unitToMs).
var resetUnits = map[string]time.Duration{
	"ms": time.Millisecond,
	"s":  time.Second, "sec": time.Second, "secs": time.Second, "second": time.Second, "seconds": time.Second,
	"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
}

// textReset is how long the provider's words say a limit lasts: the pairs after "resets in", summed ("2hr 15min" is
// 2h15m); zero when they name none.
func textReset(text string) time.Duration {
	loc := resetsIn.FindStringIndex(text)
	if loc == nil {
		return 0
	}

	rest := text[loc[1]:]

	var total time.Duration

	for {
		m := resetPair.FindStringSubmatchIndex(rest)
		if m == nil {
			break
		}

		n, err := strconv.ParseFloat(rest[m[2]:m[3]], 64)
		unit, ok := resetUnits[strings.ToLower(rest[m[4]:m[5]])]

		if err != nil || !ok || math.IsInf(n, 0) || math.IsNaN(n) {
			break
		}

		total += time.Duration(n * float64(unit))
		rest = rest[m[1]:]
	}

	return total
}

// providerReset is when the provider says a refused account may serve again: its headers first, in omp's order
// (fetch-retry.ts:80-117 extractRetryHint: retry-after-ms, Retry-After as seconds or a date, x-ratelimit-reset-ms,
// x-ratelimit-reset, x-ratelimit-reset-after), else the reset its words name (textReset); zero when it names none.
func providerReset(h http.Header, body []byte, now time.Time) time.Time {
	if at := headerReset(h, now); !at.IsZero() {
		return at
	}

	if wait := textReset(string(body)); wait > 0 {
		return now.Add(wait)
	}

	return time.Time{}
}

// headerReset is the reset the headers name, in omp's order (fetch-retry.ts:80-117); zero when none does.
func headerReset(h http.Header, now time.Time) time.Time {
	number := func(name string) (float64, bool) {
		value := strings.TrimSpace(h.Get(name))
		if value == "" {
			return 0, false
		}

		n, err := strconv.ParseFloat(value, 64)

		return n, err == nil && !math.IsInf(n, 0) && !math.IsNaN(n)
	}

	after := func(d time.Duration) time.Time {
		if d <= 0 {
			return time.Time{}
		}

		return now.Add(d)
	}

	// epoch is a reset given as a Unix time in milliseconds or seconds, by its size (fetch-retry.ts:96-98).
	epoch := func(n float64) (time.Time, bool) {
		switch {
		case n > 1e12:
			return time.UnixMilli(int64(n)), true
		case n > 1e9:
			return time.Unix(int64(n), 0), true
		default:
			return time.Time{}, false
		}
	}

	if ms, ok := number("Retry-After-Ms"); ok && ms >= 0 {
		return after(time.Duration(ms * float64(time.Millisecond)))
	}

	if value := strings.TrimSpace(h.Get("Retry-After")); value != "" {
		if secs, err := strconv.ParseFloat(value, 64); err == nil {
			return after(time.Duration(secs * float64(time.Second)))
		}

		if at, err := http.ParseTime(value); err == nil {
			return after(at.Sub(now))
		}
	}

	if n, ok := number("X-Ratelimit-Reset-Ms"); ok && n > 0 {
		if at, ok := epoch(n); ok {
			if at.After(now) {
				return at
			}
		} else {
			return after(time.Duration(n * float64(time.Millisecond)))
		}
	}

	if n, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-Ratelimit-Reset")), 10, 64); err == nil {
		if at := time.Unix(n, 0); at.After(now) {
			return at
		}
	}

	if secs, ok := number("X-Ratelimit-Reset-After"); ok && secs > 0 {
		return after(time.Duration(secs * float64(time.Second)))
	}

	return time.Time{}
}
