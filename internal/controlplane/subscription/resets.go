package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Limit resets (issue #199, decision 17). A vendor gives some accounts a
// few resets: using one starts the account's used-up limits again at once,
// and it does not come back. The references that show them: 9router-go
// (internal/codexquota/resetcredits.go, resetcredits_fetch.go, for Codex),
// 9router (open-sse/services/usage/claude.js, parseClaudeResetGrants and
// consumeClaudeResetGrant) and T3 Code (apps/server/src/provider/
// claudeResetCredits.ts) for Claude, Orca (status-bar/CodexResetCredits.tsx).

// Resets is what an account has of limit resets.
type Resets struct {
	// Available is how many the account can use.
	Available int
	// ExpiresAt is when the one used next ends; zero when the vendor did not say.
	ExpiresAt time.Time
	// next is the vendor's id of the one used next. It stays on the server:
	// 9router-go keeps a credit's id from the browser the same way.
	next string
}

var (
	// ErrNoResets is returned when the account has no reset it can use.
	ErrNoResets = errors.New("the account has no limit reset to use")
	// ErrNothingToReset is returned when no limit of the account is used up.
	ErrNothingToReset = errors.New("no limit of the account is used up, so there is nothing to reset")
	// ErrResetWait is returned when the vendor asks to wait before a reset is used.
	ErrResetWait = errors.New("the vendor asks to wait before a reset is used")
	// ErrBadResetRequest is returned for a request id the vendors would not take.
	ErrBadResetRequest = errors.New("a reset's request id is letters, digits, - and _, at most 64")
)

// ResetLister is a Vendor that lists an account's resets with a call of its own.
type ResetLister interface {
	Resets(ctx context.Context, token Token) (Resets, error)
}

// limitsWithResets is a Vendor whose limits call answers the resets too, so
// that the two are read with one call.
type limitsWithResets interface {
	limitsAndResets(ctx context.Context, token Token) ([]Window, Resets, error)
}

// ResetUser is a Vendor at which a reset can be used.
type ResetUser interface {
	// UseReset uses the reset with the vendor's id. requestID makes a
	// repeated call the same use: a retry never uses two.
	UseReset(ctx context.Context, token Token, id, requestID string) error
}

var (
	resetIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	resetRequestIDPat = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// readResets reads an account's resets after its windows were read. A vendor
// that gives none, or a call that fails, leaves the account without any: the
// windows are still shown.
func readResets(ctx context.Context, vendor Vendor, token Token) Resets {
	lister, ok := vendor.(ResetLister)
	if !ok {
		return Resets{}
	}

	resets, err := lister.Resets(ctx, token)
	if err != nil {
		return Resets{}
	}

	return resets
}

// UseReset uses one of an account's limit resets and returns the account's
// limits as they are after it. The reset used is the one the vendor says is
// next, read now, so that one the vendor would refuse is not asked for
// (9router-go lists before it consumes). requestID is the caller's: the same
// id sent again uses no second reset.
func (s *Service) UseReset(ctx context.Context, id, requestID string) (Limits, error) {
	if !resetRequestIDPat.MatchString(requestID) {
		return Limits{}, ErrBadResetRequest
	}

	a, err := s.Get(ctx, id)
	if err != nil {
		return Limits{}, err
	}

	user, ok := s.vendors[a.Runtime].(ResetUser)
	if !ok {
		return Limits{}, fmt.Errorf("%w: %s gives none", ErrNoResets, a.Runtime)
	}

	limits, err := s.Limits(ctx, id, true)
	if err != nil {
		return Limits{}, err
	}

	if limits.Resets.Available <= 0 || limits.Resets.next == "" {
		return Limits{}, ErrNoResets
	}

	token, err := s.usableToken(ctx, a)
	if err != nil {
		return Limits{}, err
	}

	if err := user.UseReset(ctx, token, limits.Resets.next, requestID); err != nil {
		return Limits{}, err
	}

	// What was read before the reset is no longer true (9router drops its
	// reading the same way).
	s.changeLimits(id, func(e *limitsEntry) { e.read, e.limits, e.heard = false, Limits{}, Limits{} })

	after, err := s.Limits(ctx, id, true)
	if err != nil {
		// The reset was used; only the new reading failed.
		return Limits{}, nil //nolint:nilerr // the reset itself succeeded
	}

	return after, nil
}

// postJSON sends a JSON body and returns the status and the answer's body.
func postJSON(ctx context.Context, client *http.Client, endpoint string, header http.Header, body any,
) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limitsTimeout)
	defer cancel()

	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}

	req.Header = header
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("using the reset: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxLimitsBytes))

	return resp.StatusCode, answer, nil
}

// --- Codex -----------------------------------------------------------------

func (c *Codex) resetHeader(token Token) http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token.AccessToken)

	if token.ExternalID != "" {
		header.Set("Chatgpt-Account-Id", token.ExternalID)
	}

	return header
}

// Resets implements ResetLister with ChatGPT's list of reset credits
// (9router-go resetcredits.go). The answer has been a bare list and a list
// under several names, and a credit's fields have had two spellings, so all
// of those are read. A credit that is used, expired or marked unavailable is
// left out; the one that ends soonest is used next.
func (c *Codex) Resets(ctx context.Context, token Token) (Resets, error) {
	body, err := getLimits(ctx, c.client, c.usageBase+"/backend-api/wham/rate-limit-reset-credits", c.resetHeader(token))
	if err != nil {
		return Resets{}, err
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return Resets{}, fmt.Errorf("reading Codex's resets: %w", err)
	}

	type credit struct {
		id      string
		expires time.Time
	}

	var credits []credit

	for _, item := range codexCreditList(payload) {
		record, ok := item.(map[string]any)
		if !ok || codexCreditUnusable(record) {
			continue
		}

		id := firstString(record, "credit_id", "creditId", "id")
		if !resetIDPattern.MatchString(id) {
			continue
		}

		expires := time.Time{}
		if text := firstString(record, "expires_at", "expiresAt", "expiration_at", "expirationAt"); text != "" {
			expires, _ = time.Parse(time.RFC3339, text)
			if !expires.IsZero() && !expires.After(c.clock.Now()) {
				continue
			}
		}

		credits = append(credits, credit{id: id, expires: expires})
	}

	if len(credits) == 0 {
		return Resets{}, nil
	}

	// The soonest to end first; one with no end last.
	sort.SliceStable(credits, func(i, j int) bool {
		switch {
		case credits[i].expires.IsZero():
			return false
		case credits[j].expires.IsZero():
			return true
		default:
			return credits[i].expires.Before(credits[j].expires)
		}
	})

	return Resets{Available: len(credits), ExpiresAt: credits[0].expires, next: credits[0].id}, nil
}

func codexCreditList(payload any) []any {
	if list, ok := payload.([]any); ok {
		return list
	}

	record, ok := payload.(map[string]any)
	if !ok {
		return nil
	}

	for _, key := range []string{
		"credits", "reset_credits", "resetCredits", "rate_limit_reset_credits", "rateLimitResetCredits", "items", "data",
	} {
		if list, ok := record[key].([]any); ok {
			return list
		}
	}

	return nil
}

func codexCreditUnusable(record map[string]any) bool {
	switch foldOutcome(firstString(record, "status", "state", "outcome", "result", "code")) {
	case "consumed", "redeeming", "redeemed", "used", "expired", "unavailable":
		return true
	}

	for key, unusable := range map[string]bool{"consumed": true, "redeemed": true, "available": false} {
		if v, ok := record[key].(bool); ok && v == unusable {
			return true
		}
	}

	return false
}

func firstString(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := record[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

// foldOutcome keeps an outcome's letters and digits in lower case, so that
// "already_redeemed", "alreadyRedeemed" and "ALREADY REDEEMED" are one word.
func foldOutcome(value string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}

	return b.String()
}

// codexOutcome reads the outcome of a consume answer: a bare word, or one
// under a known key.
func codexOutcome(raw []byte) string {
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		return foldOutcome(string(raw))
	}

	switch v := payload.(type) {
	case string:
		return foldOutcome(v)
	case map[string]any:
		return foldOutcome(firstString(v, "code", "outcome", "status", "result", "type"))
	default:
		return ""
	}
}

// UseReset implements ResetUser (9router-go ConsumeResetCredit): the credit
// and the request's id are sent, and "already redeemed" is the reset done.
func (c *Codex) UseReset(ctx context.Context, token Token, id, requestID string) error {
	if !resetIDPattern.MatchString(id) {
		return fmt.Errorf("%w: Codex named a reset this call cannot send", ErrNoResets)
	}

	status, raw, err := postJSON(ctx, c.client, c.usageBase+"/backend-api/wham/rate-limit-reset-credits/consume",
		c.resetHeader(token), map[string]string{"redeem_request_id": requestID, "credit_id": id})
	if err != nil {
		return err
	}

	switch outcome := codexOutcome(raw); {
	case outcome == "nocredit" || outcome == "nocredits":
		return ErrNoResets
	case outcome == "nothingtoreset":
		return ErrNothingToReset
	case status == http.StatusTooManyRequests:
		return ErrResetWait
	case status < 200 || status >= 300:
		return fmt.Errorf("Codex answered %d to the reset", status) //nolint:staticcheck // Codex is a name
	case outcome == "reset" || outcome == "alreadyredeemed":
		return nil
	default:
		return errors.New("Codex gave an answer to the reset that is not known; look at the account's limits") //nolint:staticcheck // Codex is a name
	}
}

// --- Claude ----------------------------------------------------------------

// claudeResetProgram is Anthropic's name for the resets it gives.
const claudeResetProgram = "cedar_ember"

func (c *Claude) usageHeader(token Token) http.Header {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token.AccessToken)
	header.Set("Anthropic-Beta", "oauth-2025-04-20")
	header.Set("Anthropic-Version", "2023-06-01")
	header.Set("User-Agent", claudeLimitsUserAgent)

	return header
}

// claudeResets reads the grants of the usage answer (9router
// parseClaudeResetGrants, T3 Code claudeResetCreditsToContract): the account
// is eligible, and a grant counts when it is not paused, has resets left, is
// usable now and has not ended. The one Anthropic names as next is used next.
func claudeResets(raw json.RawMessage, now time.Time) Resets {
	var block struct {
		Eligible bool   `json:"eligible"`
		Next     string `json:"next_grant_id"`
		Grants   []struct {
			ID     string `json:"id"`
			Left   int    `json:"resets_left"`
			EndsAt string `json:"ends_at"`
			Paused bool   `json:"paused"`
			Usable *bool  `json:"usable_now"`
		} `json:"grants"`
	}

	if json.Unmarshal(raw, &block) != nil || !block.Eligible {
		return Resets{}
	}

	var out Resets

	for _, g := range block.Grants {
		ends, _ := time.Parse(time.RFC3339, g.EndsAt)

		if g.Paused || g.Left <= 0 || !resetIDPattern.MatchString(g.ID) || (g.Usable != nil && !*g.Usable) ||
			(!ends.IsZero() && !ends.After(now)) {
			continue
		}

		out.Available += g.Left

		if out.next == "" || g.ID == block.Next {
			out.next, out.ExpiresAt = g.ID, ends
		}
	}

	return out
}

// UseReset implements ResetUser (9router consumeClaudeResetGrant, T3 Code
// consumeClaudeResetCredit): the organization is read from the profile, and
// the grant is claimed there with the request's id.
func (c *Claude) UseReset(ctx context.Context, token Token, id, requestID string) error {
	if !resetIDPattern.MatchString(id) {
		return fmt.Errorf("%w: Claude named a reset this call cannot send", ErrNoResets)
	}

	var profile struct {
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}

	if err := c.getJSON(ctx, c.profileBase+"/api/oauth/profile", token.AccessToken, &profile); err != nil {
		return fmt.Errorf("reading the account's organization: %w", err)
	}

	if profile.Organization.UUID == "" {
		return errors.New("Claude's profile names no organization to use the reset in") //nolint:staticcheck // Claude is a name
	}

	endpoint := c.profileBase + "/api/organizations/" + url.PathEscape(profile.Organization.UUID) + "/reset_rate_limits"

	status, raw, err := postJSON(ctx, c.client, endpoint, c.usageHeader(token),
		map[string]string{"program": claudeResetProgram, "grant_id": id, "request_id": requestID})
	if err != nil {
		return err
	}

	switch {
	case status == http.StatusTooManyRequests:
		return ErrResetWait
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("Claude refused the account's token for the reset (%d); sign the account in again", status) //nolint:staticcheck // Claude is a name
	case status < 200 || status >= 300:
		return fmt.Errorf("Claude answered %d to the reset", status) //nolint:staticcheck // Claude is a name
	}

	var answer struct {
		Result string `json:"result"`
	}

	_ = json.Unmarshal(raw, &answer)

	switch answer.Result {
	case "reset", "already_used":
		return nil
	case "not_limited":
		return ErrNothingToReset
	case "cooldown":
		return ErrResetWait
	case "ineligible":
		return ErrNoResets
	default:
		return errors.New("Claude could not confirm the reset; look at the account's limits before trying again") //nolint:staticcheck // Claude is a name
	}
}
