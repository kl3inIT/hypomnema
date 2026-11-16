package subscription

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
)

// The groups Antigravity counts its quota in (issue #576). Each has a five-hour and a weekly window, and every model
// of a group draws on both: 9router-go reads them from retrieveUserQuotaSummary and blocks a group's models once one
// is used up (internal/handlers/chat/antigravity_quota.go, ParseWeeklyQuotaSummary and IsAntigravityModelBlocked).
const (
	groupGemini    = "Gemini"
	groupClaudeGPT = "Claude & GPT"
	groupArgon     = "Gemini 4 Argon"
)

// Limits implements LimitReader: the windows of each group the account's quota is counted in, as the summary
// gives them.
func (a *Antigravity) Limits(ctx context.Context, token Token) ([]Window, error) {
	var (
		answer quotaSummary
		failed []error
	)

	for _, host := range a.opt.CodeAssistHosts {
		address := strings.TrimRight(host, "/") + "/" + cloudcode.APIVersion + ":retrieveUserQuotaSummary"

		err := a.call(ctx, http.MethodPost, address, token.AccessToken, map[string]string{"project": token.Project}, &answer)
		if err != nil {
			failed = append(failed, err)

			continue
		}

		windows := answer.windows()
		if len(windows) == 0 {
			return nil, errors.New("Antigravity's quota summary named no window") //nolint:staticcheck // a name
		}

		return windows, nil
	}

	return nil, fmt.Errorf("reading the account's quota: %w", errors.Join(failed...))
}

// quotaSummary is the answer of retrieveUserQuotaSummary; the groups are at the top or under quotaSummary.
type quotaSummary struct {
	Groups       []quotaGroup `json:"groups"`
	QuotaSummary *struct {
		Groups []quotaGroup `json:"groups"`
	} `json:"quotaSummary"`
}

type quotaGroup struct {
	DisplayName string `json:"displayName"`
	Buckets     []struct {
		BucketID          string   `json:"bucketId"`
		DisplayName       string   `json:"displayName"`
		Window            string   `json:"window"`
		Disabled          bool     `json:"disabled"`
		RemainingFraction *float64 `json:"remainingFraction"`
		ResetTime         string   `json:"resetTime"`
	} `json:"buckets"`
}

// windows reads the summary as 9router-go's ParseWeeklyQuotaSummary does: a group is told by its name; a bucket
// is weekly or a five-hour session by its window or its words; a disabled session bucket is used up (Google
// disables it when the week is), a disabled weekly one is left out, and one with no fraction stays unknown. The
// first bucket of each kind in a group is kept.
func (s quotaSummary) windows() []Window {
	groups := s.Groups
	if len(groups) == 0 && s.QuotaSummary != nil {
		groups = s.QuotaSummary.Groups
	}

	var (
		out  []Window
		seen = map[string]bool{}
	)

	for _, g := range groups {
		group := quotaGroupOf(g.DisplayName)
		if group == "" {
			continue
		}

		for _, b := range g.Buckets {
			words := strings.ToLower(b.BucketID + " " + b.DisplayName)
			window := strings.ToLower(b.Window)
			weekly := window == "weekly" || strings.Contains(words, "weekly")
			session := window == "5h" || window == "daily" || strings.Contains(words, "five hour") ||
				strings.Contains(words, "5h") || strings.Contains(words, "daily")

			var left float64

			switch {
			case !weekly && !session, b.Disabled && weekly:
				continue
			case b.Disabled:
				left = 0
			case b.RemainingFraction == nil:
				continue
			default:
				left = min(max(*b.RemainingFraction, 0), 1)
			}

			name := groupWindow(WindowFiveHours, group)
			if weekly {
				name = groupWindow(WindowWeek, group)
			}

			if seen[name] {
				continue
			}

			seen[name] = true

			reset, _ := time.Parse(time.RFC3339, b.ResetTime)
			out = append(out, Window{Name: name, UsedPercent: percent((1 - left) * 100), ResetsAt: reset.UTC()})
		}
	}

	return out
}

// quotaGroupOf is the group a summary's group name or a model's name belongs to; "" for none.
func quotaGroupOf(name string) string {
	name = strings.ToLower(name)

	switch {
	case strings.Contains(name, "argon"):
		return groupArgon
	case strings.Contains(name, "claude"), strings.Contains(name, "gpt"):
		return groupClaudeGPT
	case strings.Contains(name, "gemini"):
		return groupGemini
	}

	return ""
}

// groupWindow names a group's window: "5 hours (Gemini)", "week (Claude & GPT)".
func groupWindow(window, group string) string { return window + " (" + group + ")" }

// UsedUpUntil reports until when an account cannot serve a model because a window of the model's group is used
// up (issue #576), from the account's last reading of its limits; zero when none is, or nothing is known.
func (s *Service) UsedUpUntil(ctx context.Context, id, model string) time.Time {
	group := quotaGroupOf(model)
	if group == "" {
		return time.Time{}
	}

	limits, err := s.Limits(ctx, id, false)
	if err != nil {
		return time.Time{}
	}

	now := s.clock.Now()

	var until time.Time

	for _, w := range limits.Windows {
		if !strings.HasSuffix(w.Name, " ("+group+")") || w.UsedPercent < 100 || !w.ResetsAt.After(now) {
			continue
		}

		if w.ResetsAt.After(until) {
			until = w.ResetsAt
		}
	}

	return until
}
