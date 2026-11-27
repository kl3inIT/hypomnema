package muse

// The Usage of an account as Muse's Settings → Usage shows it (issue #782,
// row 9): "Free plan / Weekly limit resets on Sep 30 / 1% used / Additional
// tokens / Never expires / 0% used (2B tokens left)". Parsed as www222fff's
// muse2api parses it (src/muse2api/drivers/browser/dom.py:246-277,
// parse_usage_text, and tests/test_quota_parse.py), with the Vietnamese
// wording observed on zm. The reset
// is kept as the text Muse shows: no timestamp is made of it, since the text
// names no year and no time zone.

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Usage is what Settings → Usage said.
type Usage struct {
	Plan string `json:"plan,omitempty"`
	// UsedPercent is the weekly limit's share used; Found is false when it was not read.
	UsedPercent float64 `json:"used_percent"`
	// Reset is the reset's text as shown ("Sep 30"); empty when not shown.
	Reset string `json:"reset,omitempty"`
	// ExtraLeft is what the additional tokens line says is left.
	ExtraLeft string `json:"extra_left,omitempty"`
	Found     bool   `json:"found"`
}

var (
	usagePlan  = regexp.MustCompile(`(?i)^(?:.{1,60}?\s+plan|Gói\s+.{1,60})$`)
	usageReset = regexp.MustCompile(`(?i)(?:Weekly limit resets? on|Giới hạn hàng tuần sẽ đặt lại vào) ([^\n]+)`)
	usageExtra = regexp.MustCompile(`(?i)^(?:(?:Additional|Extra)\s+tokens|Token bổ sung)`)
	usageUsed  = regexp.MustCompile(`(?i)(?:(\d+(?:\.\d+)?)\s*%\s*used\b|Đã dùng\s+(\d+(?:[.,]\d+)?)\s*%)`)
	usageLeft  = regexp.MustCompile(`\(([^)]+)\)`)
)

// ParseUsage reads the Usage section of the settings dialog's text. Found is
// set only when the weekly share was read: a plan alone is no reading.
func ParseUsage(text string) Usage {
	var lines []string

	for _, line := range strings.Split(norm.NFKC.String(text), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}

	// From the Usage heading to the Language one.
	for i, line := range lines {
		if strings.EqualFold(line, "usage") || strings.EqualFold(line, "Mức sử dụng") {
			lines = lines[i+1:]

			break
		}
	}

	for i, line := range lines {
		if strings.EqualFold(line, "language") || strings.EqualFold(line, "Ngôn ngữ") {
			lines = lines[:i]

			break
		}
	}

	var u Usage

	for _, line := range lines {
		if usagePlan.MatchString(line) {
			u.Plan = line

			break
		}
	}

	if m := usageReset.FindStringSubmatch(strings.Join(lines, "\n")); m != nil {
		u.Reset = strings.TrimSpace(m[1])
	}

	extra := len(lines)

	for i, line := range lines {
		if usageExtra.MatchString(line) {
			extra = i

			break
		}
	}

	for _, line := range lines[:extra] {
		if pct, ok := usedPercent(line); ok {
			u.UsedPercent, u.Found = pct, true

			break
		}
	}

	for _, line := range lines[extra:] {
		if _, ok := usedPercent(line); ok {
			if m := usageLeft.FindStringSubmatch(line); m != nil {
				u.ExtraLeft = strings.TrimSpace(m[1])
			}

			break
		}
	}

	return u
}

func usedPercent(line string) (float64, bool) {
	m := usageUsed.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}

	v, err := strconv.ParseFloat(strings.ReplaceAll(cmp.Or(m[1], m[2]), ",", "."), 64)
	if err != nil || v < 0 || v > 100 {
		return 0, false
	}

	return v, true
}

// Room says whether the weekly limit has room left.
func (u Usage) Room() bool { return u.Found && u.UsedPercent < 100 }

// Text is the usage as the Muse page shows it: "62% used · resets Sep 30",
// with "unknown" for what was not read.
func (u Usage) Text() string {
	if !u.Found {
		return "unknown"
	}

	reset := u.Reset
	if reset == "" {
		reset = "unknown"
	}

	return fmt.Sprintf("%s%% used · resets %s", strconv.FormatFloat(u.UsedPercent, 'f', -1, 64), reset)
}
