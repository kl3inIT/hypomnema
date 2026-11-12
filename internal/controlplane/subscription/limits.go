package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The limits of a subscription (issue #199, decisions 7 and 8): how much of
// each of its windows an account has used, and when the window starts again.
// Claude and Codex each answer one call made with the account's own token;
// neither address is a published API, so a reading that fails is left out
// and nothing else depends on it.
//
// Read in: 9router-go (internal/codexquota/fetch.go and wire.go,
// internal/handlers/dashboard/usage_claude.go), Orca
// (src/main/rate-limits/providers/claude/claude-oauth-usage-request.ts,
// providers/codex/codex-backend-usage-client.ts) and OpenMausBot
// (server/plan-usage.ts). All three call the same two addresses.

const (
	// limitsKept is how long a reading is given again without asking, and
	// limitsRest how long an account is not asked after a 429 that names no
	// wait: the endpoint of Claude answers 429 when asked often (9router-go
	// usage_claude.go, claudeUsageTTL and claudeUsage429Cooldown).
	limitsKept = 60 * time.Second
	limitsRest = 3 * time.Minute
	// limitsLongestRest bounds a Retry-After that is believed (9router-go: 900 s).
	limitsLongestRest = 15 * time.Minute
	limitsTimeout     = 15 * time.Second
	maxLimitsBytes    = 1 << 20

	// The names of the two windows every subscription has.
	WindowFiveHours = "5 hours"
	WindowWeek      = "week"
)

// Window is one window of a subscription's limits.
type Window struct {
	// Name is "5 hours", "week", or "week (Opus)" for a model's own week.
	Name string
	// UsedPercent is the part used, from 0 to 100.
	UsedPercent float64
	// ResetsAt is when the window starts again; zero when the vendor did not say.
	ResetsAt time.Time
}

// Limits is a reading of an account's windows.
type Limits struct {
	Windows []Window
	// ReadAt is when the vendor answered.
	ReadAt time.Time
	// Resets are the limit resets the account can use (resets.go); none for a
	// vendor that gives none, or when they could not be read.
	Resets Resets
}

// LimitReader is a Vendor that can say how much of an account's limits is used.
type LimitReader interface {
	Limits(ctx context.Context, token Token) ([]Window, error)
}

// ErrNoLimits is returned for an account whose vendor does not say its limits.
var ErrNoLimits = errors.New("this kind of account does not say its limits")

// ErrLimitsResting is returned while an account is not asked after a 429 and
// has no earlier reading to give.
var ErrLimitsResting = errors.New("the vendor asked to wait before its limits are read again")

// limitsRefusedError is a vendor's answer other than 200 to the limits call.
type limitsRefusedError struct {
	status int
	// wait is the Retry-After of a 429, zero when it named none.
	wait time.Duration
	// said is the vendor's own message, when its answer had one.
	said string
}

func (e *limitsRefusedError) Error() string {
	switch e.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		// The vendor's words are given, as Orca shows them for a token that
		// lacks the scope (claude-active-usage-fetch.ts, 'missing-scope'): a
		// token pasted by hand serves models and may still not read limits.
		said := ""
		if e.said != "" {
			said = ": " + e.said
		}

		return fmt.Sprintf("the vendor refused the account's token for its limits (%d)%s; a token added by hand may "+
			"lack what this call needs: log the account in with `accounts login` to see its limits", e.status, said)
	default:
		return fmt.Sprintf("the vendor answered %d to the limits call", e.status)
	}
}

// limitsEntry is what the service keeps of an account's limits, in memory as
// the references keep theirs.
type limitsEntry struct {
	limits Limits
	read   bool
	// restUntil is until when the vendor is not asked, after a 429.
	restUntil time.Time
	// refused is the vendor's refusal of the token whose version is
	// refusedVersion: it is not asked again with that token, as Orca ends
	// there for a token without the scope (claude-usage-error-classification.ts,
	// terminal). A new login or a refresh makes a new version.
	refused        error
	refusedVersion int
	// heard is what the vendor said of the windows on an answer served
	// under the account (NoteAnswer).
	heard Limits
}

// Limits says how much of an account's windows is used. A reading younger
// than a minute is given again unless fresh asks for a new one. When the
// vendor cannot be asked or fails, the last reading is given if there is one,
// as 9router-go does: a failed call does not blank what was known. With no
// reading of the call, what the vendor said on the account's last answer is
// given (NoteAnswer), as omp reads the limits from an answer's headers.
func (s *Service) Limits(ctx context.Context, id string, fresh bool) (Limits, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Limits{}, err
	}

	reader, ok := s.vendors[a.Runtime].(LimitReader)
	if !ok {
		return Limits{}, fmt.Errorf("%w: %s", ErrNoLimits, a.Runtime)
	}

	now := s.clock.Now()

	s.limitsMu.Lock()
	entry := s.limits[id]
	s.limitsMu.Unlock()

	if entry.read && !fresh && now.Sub(entry.limits.ReadAt) < limitsKept {
		return entry.limits, nil
	}

	if now.Before(entry.restUntil) {
		return entry.orElse(ErrLimitsResting, now)
	}

	token, err := s.usableToken(ctx, a)
	if err != nil {
		return Limits{}, err
	}

	if entry.refused != nil && entry.refusedVersion == token.Version {
		return entry.orElse(entry.refused, now)
	}

	var (
		windows []Window
		resets  Resets
	)

	if both, ok := reader.(limitsWithResets); ok {
		windows, resets, err = both.limitsAndResets(ctx, token)
	} else {
		windows, err = reader.Limits(ctx, token)
	}

	if err != nil {
		var refused *limitsRefusedError

		switch {
		case !errors.As(err, &refused):
		case refused.status == http.StatusTooManyRequests:
			wait := limitsRest
			if refused.wait > 0 && refused.wait <= limitsLongestRest {
				wait = refused.wait
			}

			s.changeLimits(id, func(e *limitsEntry) { e.restUntil = now.Add(wait) })
		case refused.status == http.StatusUnauthorized || refused.status == http.StatusForbidden:
			s.changeLimits(id, func(e *limitsEntry) { e.refused, e.refusedVersion = err, token.Version })
		}

		return entry.orElse(err, now)
	}

	if _, ok := reader.(limitsWithResets); !ok {
		resets = readResets(ctx, s.vendors[a.Runtime], token)
	}

	limits := Limits{Windows: windows, ReadAt: now, Resets: resets}
	s.changeLimits(id, func(e *limitsEntry) {
		e.limits, e.read, e.restUntil, e.refused = limits, true, time.Time{}, nil
	})

	return limits, nil
}

// orElse is the last reading of the call, or what was heard on an answer, or
// the error. A window heard whose reset has passed has started again and is
// left out: what it holds now is not known (inferred).
func (e limitsEntry) orElse(err error, now time.Time) (Limits, error) {
	if e.read {
		return e.limits, nil
	}

	var still []Window

	for _, w := range e.heard.Windows {
		if w.ResetsAt.IsZero() || w.ResetsAt.After(now) {
			still = append(still, w)
		}
	}

	if len(still) > 0 {
		return Limits{Windows: still, ReadAt: e.heard.ReadAt}, nil
	}

	return Limits{}, err
}

func (s *Service) changeLimits(id string, change func(*limitsEntry)) {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()

	entry := s.limits[id]
	change(&entry)
	s.limits[id] = entry
}

// LimitHeaders is a Vendor that says an account's windows in the headers of
// each answer it serves.
type LimitHeaders interface {
	LimitsInHeaders(h http.Header) []Window
}

// NoteAnswer keeps what the headers of an answer served under an account say
// of its windows, for when the limits call cannot be made with the account's
// token. An answer that names no window changes nothing.
func (s *Service) NoteAnswer(runtime, accountID string, h http.Header) {
	vendor, ok := s.vendors[runtime].(LimitHeaders)
	if !ok {
		return
	}

	windows := vendor.LimitsInHeaders(h)
	if len(windows) == 0 {
		return
	}

	heard := Limits{Windows: windows, ReadAt: s.clock.Now()}
	s.changeLimits(accountID, func(e *limitsEntry) { e.heard = heard })
}

// LimitsInHeaders implements LimitHeaders: Anthropic names each unified
// window on every answer, its used part from 0 to 1 and its reset in seconds
// since 1970 (omp packages/ai/src/usage/claude.ts, parseUnifiedWindow and
// parseClaudeRateLimitHeaders: 5h, 7d, and 7d_oi, the week of one model,
// which omp calls Fable's).
func (c *Claude) LimitsInHeaders(h http.Header) []Window {
	var windows []Window

	for _, w := range []struct{ key, name string }{
		{"5h", WindowFiveHours}, {"7d", WindowWeek}, {"7d_oi", modelWeek("fable")},
	} {
		prefix := "Anthropic-Ratelimit-Unified-" + w.key + "-"

		used, err := strconv.ParseFloat(strings.TrimSpace(h.Get(prefix+"Utilization")), 64)
		if err != nil {
			continue
		}

		windows = append(windows, Window{
			Name: w.name, UsedPercent: percent(used * 100),
			ResetsAt: resetTime(json.RawMessage(strings.TrimSpace(h.Get(prefix + "Reset")))),
		})
	}

	return windows
}

// usableToken is an account's access token, refreshed first when it is about
// to end.
func (s *Service) usableToken(ctx context.Context, a Account) (Token, error) {
	if a.State != StateActive {
		return Token{}, fmt.Errorf("%w: account %s is %s", ErrNoTokens, a.ID, cmpState(a.State))
	}

	if a.AccessTokenExpiresAt != nil && a.AccessTokenExpiresAt.Sub(s.clock.Now()) < useLead {
		if err := s.refresh(ctx, a, 0); err != nil {
			return Token{}, err
		}
	}

	return s.tokenOf(ctx, a.Account)
}

// getLimits makes the limits call and returns its body, or a limitsRefusedError.
func getLimits(ctx context.Context, client *http.Client, endpoint string, header http.Header) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limitsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	req.Header = header
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading the limits: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	if resp.StatusCode != http.StatusOK {
		refused := &limitsRefusedError{status: resp.StatusCode}
		if seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && seconds > 0 {
			refused.wait = time.Duration(seconds) * time.Second
		}

		// The vendor's message, as Orca reads it (claude-oauth-usage-error.ts,
		// describeOAuthUsageError); cut short, since it is shown.
		var answer struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}

		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(raw, &answer) == nil {
			refused.said = clip(strings.TrimSpace(answer.Error.Message))
		}

		return nil, refused
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxLimitsBytes))
}

// claudeUsageWindow is one window of Anthropic's answer: utilization is the
// percent used, and resets_at a time in RFC 3339 or in seconds.
type claudeUsageWindow struct {
	Utilization *float64        `json:"utilization"`
	ResetsAt    json.RawMessage `json:"resets_at"`
}

// Limits implements LimitReader with Anthropic's OAuth usage call, which
// answers Claude Code's own client only: the beta flag and the version are
// what the CLI sends (9router-go usage_claude.go; Orca and OpenMausBot send
// the same beta flag). Besides the two windows of every plan it gives a
// week of its own to some models, as `seven_day_<model>` and as an entry of
// `limits` of kind weekly_scoped (Orca claude-oauth-usage-request.ts,
// mapFableWeeklyWindow; OpenMausBot plan-usage.ts, parseClaudeUsage).
func (c *Claude) Limits(ctx context.Context, token Token) ([]Window, error) {
	windows, _, err := c.limitsAndResets(ctx, token)

	return windows, err
}

// limitsAndResets reads the windows and the account's limit resets with one
// call: asked with cedar_ember=1, as Claude Code asks, the usage answer names
// the resets too (9router open-sse/services/usage/claude.js). The call is
// refused when made often, so the two are not asked for apart.
func (c *Claude) limitsAndResets(ctx context.Context, token Token) ([]Window, Resets, error) {
	windows, answer, err := c.usageWindows(ctx, token)
	if err != nil {
		return nil, Resets{}, err
	}

	return windows, claudeResets(answer[claudeResetProgram], c.clock.Now()), nil
}

func (c *Claude) usageWindows(ctx context.Context, token Token) ([]Window, map[string]json.RawMessage, error) {
	body, err := getLimits(ctx, c.client, c.profileBase+"/api/oauth/usage?"+claudeResetProgram+"=1", c.usageHeader(token))
	if err != nil {
		return nil, nil, err
	}

	var answer map[string]json.RawMessage
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, nil, fmt.Errorf("reading Claude's limits: %w", err)
	}

	var windows []Window

	add := func(name string, raw json.RawMessage) {
		var w claudeUsageWindow
		if json.Unmarshal(raw, &w) != nil || w.Utilization == nil {
			return
		}

		windows = append(windows, Window{Name: name, UsedPercent: percent(*w.Utilization), ResetsAt: resetTime(w.ResetsAt)})
	}

	add(WindowFiveHours, answer["five_hour"])
	add(WindowWeek, answer["seven_day"])

	var scoped []string

	for key := range answer {
		if strings.HasPrefix(key, "seven_day_") {
			scoped = append(scoped, key)
		}
	}

	sort.Strings(scoped)

	for _, key := range scoped {
		add(modelWeek(strings.TrimPrefix(key, "seven_day_")), answer[key])
	}

	var limits []struct {
		Kind     string          `json:"kind"`
		Percent  *float64        `json:"percent"`
		ResetsAt json.RawMessage `json:"resets_at"`
		Scope    struct {
			Model struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	}

	_ = json.Unmarshal(answer["limits"], &limits)

	for _, l := range limits {
		name := modelWeek(l.Scope.Model.DisplayName)
		if l.Kind != "weekly_scoped" || l.Percent == nil || l.Scope.Model.DisplayName == "" || hasWindow(windows, name) {
			continue
		}

		windows = append(windows, Window{Name: name, UsedPercent: percent(*l.Percent), ResetsAt: resetTime(l.ResetsAt)})
	}

	if len(windows) == 0 {
		return nil, nil, errors.New("Claude's answer named no window") //nolint:staticcheck // Claude is a name
	}

	return windows, answer, nil
}

// claudeLimitsUserAgent is Claude Code's, as 9router-go sends it to this call.
const claudeLimitsUserAgent = "claude-cli/2.1.280 (external, cli)"

// modelWeek names a model's own week: "week (Opus)".
func modelWeek(model string) string {
	model = strings.TrimSpace(strings.ReplaceAll(model, "_", " "))
	if model == "" {
		return WindowWeek
	}

	return WindowWeek + " (" + strings.ToUpper(model[:1]) + model[1:] + ")"
}

func hasWindow(windows []Window, name string) bool {
	for _, w := range windows {
		if strings.EqualFold(w.Name, name) {
			return true
		}
	}

	return false
}

// codexUsageBase is where ChatGPT answers the usage of a Codex subscription.
const codexUsageBase = "https://chatgpt.com"

// Limits implements LimitReader with ChatGPT's usage call: a bearer token and,
// when the account has one, its ChatGPT account id (Orca codex-backend-auth.ts
// and OpenMausBot plan-usage.ts send it; 9router-go sends the bearer alone).
// The answer has a primary and a secondary window, each with the percent used,
// its length in seconds and its reset in seconds since 1970. Which is the
// 5 hours and which the week is told by the length, as Orca classifies them
// (codex-rate-limit-window-classification.ts); a window with no length is
// taken by its place.
func (c *Codex) Limits(ctx context.Context, token Token) ([]Window, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token.AccessToken)

	if token.ExternalID != "" {
		header.Set("Chatgpt-Account-Id", token.ExternalID)
	}

	body, err := getLimits(ctx, c.client, c.usageBase+"/backend-api/wham/usage", header)
	if err != nil {
		return nil, err
	}

	type window struct {
		UsedPercent *float64        `json:"used_percent"`
		Seconds     float64         `json:"limit_window_seconds"`
		ResetAt     json.RawMessage `json:"reset_at"`
	}

	var answer struct {
		RateLimit struct {
			Primary   *window `json:"primary_window"`
			Secondary *window `json:"secondary_window"`
		} `json:"rate_limit"`
	}

	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("reading Codex's limits: %w", err)
	}

	var windows []Window

	for i, w := range []*window{answer.RateLimit.Primary, answer.RateLimit.Secondary} {
		if w == nil || w.UsedPercent == nil {
			continue
		}

		name := WindowFiveHours
		if w.Seconds > (24*time.Hour).Seconds() || (w.Seconds <= 0 && i == 1) {
			name = WindowWeek
		}

		windows = append(windows, Window{Name: name, UsedPercent: percent(*w.UsedPercent), ResetsAt: resetTime(w.ResetAt)})
	}

	if len(windows) == 0 {
		return nil, errors.New("Codex's answer named no window") //nolint:staticcheck // Codex is a name
	}

	return windows, nil
}

// percent keeps a used part within 0 and 100, as Orca's mapClaudeUsageWindow.
func percent(used float64) float64 {
	return min(100, max(0, used))
}

// resetTime reads a reset written as RFC 3339, or as seconds or milliseconds
// since 1970, a number or a string of one (Orca claude-usage-window.ts,
// parseClaudeUsageResetTimestamp: above 1e10 it is milliseconds).
func resetTime(raw json.RawMessage) time.Time {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		text = strings.TrimSpace(string(raw))
	}

	if text == "" || text == "null" {
		return time.Time{}
	}

	if n, err := strconv.ParseFloat(text, 64); err == nil {
		if n <= 0 {
			return time.Time{}
		}

		if n > 1e10 {
			return time.UnixMilli(int64(n)).UTC()
		}

		return time.Unix(int64(n), 0).UTC()
	}

	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}

	return at.UTC()
}
