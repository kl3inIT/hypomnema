package subscription

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Which account serves a new conversation (issue #319), as omp ranks its
// credentials (packages/ai/src/auth/rank.ts, select.ts, usage-report.ts): a
// conversation keeps the account it began on while its prompt cache may still
// be warm, and a new one, or one idle past that, goes to the account whose
// windows most need using. That is the account whose week has the most left
// for the hours until it starts again ("required drain"): its quota would
// otherwise be lost at the reset, while a sibling's can wait. An account whose
// five hours are nearly used goes after the others, so a conversation does not
// begin where it would soon be stopped; one in reserve goes last, as before.

const (
	// hotUsed is the part of the five hours used at or above which an account
	// goes after cooler ones (omp rank.ts PRIMARY_WINDOW_HOT_FRACTION).
	hotUsed = 0.85
	// claudeWarm is how long after its last request a conversation's prompt
	// cache is held to be warm on Anthropic, and the conversation kept on its
	// account (omp usage/claude.ts stickyWarmMs, an hour). A runtime without a
	// known warm time keeps its conversations on their accounts while they last.
	claudeWarm = time.Hour
	// pinsKept is how long a conversation's account is remembered after its last
	// request (inferred: a day, so the map does not grow without end).
	pinsKept = 24 * time.Hour
)

// warmFor is how long a runtime's conversation stays on its account after its
// last request; zero is for as long as it is remembered.
func warmFor(runtime string) time.Duration {
	if runtime == "claude" {
		return claudeWarm
	}

	return 0
}

// pin is the account a conversation is on, and when it last used it.
type pin struct {
	account string
	used    time.Time
}

// standing is what ranks an account: the reserve, a hot five hours, whether
// its windows are known, and each window's required drain and used part.
type standing struct {
	reserve, hot, measured bool
	weekDrain, weekUsed    float64
	fiveDrain, fiveUsed    float64
}

// standingOf reads an account's windows as omp's ranking reads them.
func (s *Service) standingOf(id string, now time.Time) standing {
	st := standing{reserve: s.inReserve(id, now)}

	for _, w := range s.windowsFor(id, now) {
		used := w.UsedPercent / 100

		switch w.Name {
		case WindowFiveHours:
			st.measured, st.fiveUsed, st.fiveDrain = true, used, requiredDrain(used, w.ResetsAt, 5*time.Hour, now)
			st.hot = used >= hotUsed
		case WindowWeek:
			st.measured, st.weekUsed, st.weekDrain = true, used, requiredDrain(used, w.ResetsAt, 7*24*time.Hour, now)
		}
	}

	return st
}

// requiredDrain is what is left of a window for each hour until it starts
// again, the rate at which it must be used not to lose any (omp usage-report.ts
// windowRequiredDrain). A window that does not say when it resets is given its
// whole length; the hours are at least a minute's.
func requiredDrain(used float64, resetsAt time.Time, length time.Duration, now time.Time) float64 {
	left := 1 - used
	if left <= 0 {
		return 0
	}

	remaining := length
	if !resetsAt.IsZero() {
		remaining = min(resetsAt.Sub(now), length)
	}

	return left / max(remaining, time.Minute).Hours()
}

// compareStanding orders two accounts as omp's compareUsageRankedCandidatePriority
// does, after blocks: reserve last, a hot five hours after, measured before
// not, then the week's required drain from most, the week's used from least,
// the five hours' drain from most and used from least. Zero keeps their order.
func compareStanding(a, b standing) int {
	if c := cmpBool(a.reserve, b.reserve); c != 0 {
		return c
	}

	if c := cmpBool(a.hot, b.hot); c != 0 {
		return c
	}

	if c := cmpBool(!a.measured, !b.measured); c != 0 {
		return c
	}

	if c := cmp.Compare(b.weekDrain, a.weekDrain); c != 0 {
		return c
	}

	if c := cmp.Compare(a.weekUsed, b.weekUsed); c != 0 {
		return c
	}

	if c := cmp.Compare(b.fiveDrain, a.fiveDrain); c != 0 {
		return c
	}

	return cmp.Compare(a.fiveUsed, b.fiveUsed)
}

// rank orders a runtime's accounts for a request. A conversation whose
// account is warm keeps it first, unless that account is in reserve and a
// sibling is not (omp select.ts automaticPinWouldBeEvicted, which also asks
// the sibling be measured; the reserve here moved a conversation before this
// ranking, and still does); otherwise the accounts are ranked, ties kept in
// the order given.
func (s *Service) rank(runtime, session string, ordered []Account, now time.Time) []Account {
	standings := make(map[string]standing, len(ordered))
	for _, a := range ordered {
		standings[a.ID] = s.standingOf(a.ID, now)
	}

	if home, ok := s.warmPin(runtime, session, now); ok {
		at := slices.IndexFunc(ordered, func(a Account) bool { return a.ID == home })

		evicted := at >= 0 && standings[home].reserve && slices.ContainsFunc(ordered, func(a Account) bool {
			return a.ID != home && !standings[a.ID].reserve
		})

		if at >= 0 && !evicted {
			out := make([]Account, 0, len(ordered))
			out = append(out, ordered[at])
			out = append(out, slices.Delete(slices.Clone(ordered), at, at+1)...)

			return out
		}
	}

	ranked := slices.Clone(ordered)
	slices.SortStableFunc(ranked, func(a, b Account) int { return compareStanding(standings[a.ID], standings[b.ID]) })

	return ranked
}

func pinKey(runtime, session string) string { return runtime + "\x00" + session }

// warmPin is the account a conversation is on while its cache may be warm.
func (s *Service) warmPin(runtime, session string, now time.Time) (string, bool) {
	if session == "" {
		return "", false
	}

	s.pinsMu.Lock()
	defer s.pinsMu.Unlock()

	p, ok := s.pins[pinKey(runtime, session)]
	if !ok {
		return "", false
	}

	if warm := warmFor(runtime); warm > 0 && now.Sub(p.used) >= warm {
		return "", false
	}

	return p.account, true
}

// keepPin remembers the account a conversation was served on now, and forgets
// conversations idle for a day.
func (s *Service) keepPin(runtime, session, account string, now time.Time) {
	if session == "" {
		return
	}

	s.pinsMu.Lock()
	defer s.pinsMu.Unlock()

	for key, p := range s.pins {
		if now.Sub(p.used) >= pinsKept {
			delete(s.pins, key)
		}
	}

	s.pins[pinKey(runtime, session)] = pin{account: account, used: now}
}

// windowsFor is the newer of an account's last limits reading and what its
// last answer said, while it is trusted; a window whose reset has passed is
// left out, what it holds now not being known.
func (s *Service) windowsFor(id string, now time.Time) []Window {
	s.limitsMu.Lock()
	entry := s.limits[id]
	s.limitsMu.Unlock()

	latest := entry.heard
	if entry.read && entry.limits.ReadAt.After(latest.ReadAt) {
		latest = entry.limits
	}

	if latest.ReadAt.IsZero() || now.Sub(latest.ReadAt) >= quotaTrusted {
		return nil
	}

	var still []Window

	for _, w := range latest.Windows {
		if w.ResetsAt.IsZero() || w.ResetsAt.After(now) {
			still = append(still, w)
		}
	}

	return still
}

// LimitsInHeaders implements LimitHeaders: the Codex backend names its
// primary (five hours) and secondary (week) windows on every answer, each a
// used percentage, its length in minutes and its reset in seconds since 1970
// (omp packages/ai/src/usage/openai-codex.ts parseCodexRateLimitHeaders).
func (c *Codex) LimitsInHeaders(h http.Header) []Window {
	var windows []Window

	for i, key := range []string{"Primary", "Secondary"} {
		prefix := "X-Codex-" + key + "-"

		used, err := strconv.ParseFloat(strings.TrimSpace(h.Get(prefix+"Used-Percent")), 64)
		if err != nil {
			continue
		}

		name := WindowFiveHours
		if minutes, err := strconv.ParseFloat(strings.TrimSpace(h.Get(prefix+"Window-Minutes")), 64); err == nil {
			if minutes > 24*60 {
				name = WindowWeek
			}
		} else if i == 1 {
			name = WindowWeek
		}

		var resetsAt time.Time
		if secs, err := strconv.ParseInt(strings.TrimSpace(h.Get(prefix+"Reset-At")), 10, 64); err == nil && secs > 0 {
			resetsAt = time.Unix(secs, 0).UTC()
		}

		windows = append(windows, Window{Name: name, UsedPercent: percent(used), ResetsAt: resetsAt})
	}

	return windows
}
