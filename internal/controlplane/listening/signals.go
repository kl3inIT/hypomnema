package listening

import (
	"regexp"
	"strings"
)

// A role out of character for its company (issue #1289): last30days flags such roles on structure alone and leaves
// whether they are new for the company to the model (hiring_signals.py _strategic_candidates, _title_flags and
// _specialization, lines 375-461): a founding or first role, a role that leads a function, a role with a specialty
// named in its title, and a role in a place no other role is in while another place has three or more.

const (
	SignalFounding    = "founding"
	SignalLeadership  = "leadership"
	SignalSpecialized = "specialized"
	SignalNewGeo      = "new-geo"
)

var (
	// leadershipTerms are last30days LEADERSHIP_TERMS, with Vietnamese's "giám đốc" (director) and "trưởng phòng"
	// (head of department) (inferred: no reference reads Vietnamese titles).
	leadershipTerms = []string{
		"head of", "chief", "global head", "svp", "vp of", "vp,", "director of", "general manager", "vice president",
		"giám đốc", "trưởng phòng",
	}
	firstWord   = regexp.MustCompile(`\bfirst\b`)
	gmWord      = regexp.MustCompile(`\bgm\b`)
	parenthesis = regexp.MustCompile(`\(([^)]+)\)`)
	// genericQualifiers are level and logistics words, not a specialty (last30days _GENERIC_QUALIFIERS).
	genericQualifiers = set(strings.Fields("senior staff principal lead junior mid sr jr i ii iii iv remote hybrid " +
		"onsite on-site contract intern full-time part-time us uk emea"))
	// levelAndTeamWords say a level or a team (last30days _LEVEL_AND_TEAM_WORDS).
	levelAndTeamWords = set(strings.Fields("senior staff principal lead junior mid sr jr i ii iii iv remote hybrid " +
		"onsite on-site contract intern full-time part-time us uk emea entry level new grad graduate associate " +
		"experienced internship team teams multiple various all any org organization group department division general"))
	// regionWords are places, not specialties (last30days _REGION_WORDS).
	regionWords = set([]string{
		"apac", "latam", "americas", "north america", "europe", "global", "worldwide", "usa", "united states",
	})
)

// placeKey is the city a location starts with (last30days place_key).
func placeKey(location string) string {
	head, _, _ := strings.Cut(location, ",")

	return strings.ToLower(strings.TrimSpace(head))
}

// markSignals gives each role its flags, in last30days' order (title flags, then a new place).
func markSignals(jobs []posting) []Item {
	places := map[string]int{}
	dominant := 0

	for _, j := range jobs {
		if k := placeKey(j.location); k != "" {
			places[k]++
			dominant = max(dominant, places[k])
		}
	}

	out := make([]Item, 0, len(jobs))

	for _, j := range jobs {
		it := j.item
		it.Signals = titleFlags(it.Title, places)

		if k := placeKey(j.location); k != "" && places[k] == 1 && dominant >= 3 {
			it.Signals = append(it.Signals, SignalNewGeo)
		}

		out = append(out, it)
	}

	return out
}

func titleFlags(title string, places map[string]int) []string {
	lower := strings.ToLower(title)

	var flags []string

	if strings.Contains(lower, "founding") || firstWord.MatchString(lower) {
		flags = append(flags, SignalFounding)
	}

	if containsAny(lower, leadershipTerms) || gmWord.MatchString(lower) {
		flags = append(flags, SignalLeadership)
	}

	if specialty(title, places) != "" {
		flags = append(flags, SignalSpecialized)
	}

	return flags
}

// specialty is the specialty a title names in parentheses or after its last comma, "" for a level, a team or a place
// (last30days _specialization).
func specialty(title string, places map[string]int) string {
	tail := ""

	if m := parenthesis.FindStringSubmatch(title); m != nil {
		tail = strings.TrimSpace(m[1])
	} else if i := strings.LastIndex(title, ","); i >= 0 {
		tail = strings.TrimSpace(title[i+1:])
	}

	lower := strings.ToLower(tail)
	if len([]rune(tail)) < 4 || genericQualifiers[lower] || regionWords[lower] || places[placeKey(lower)] > 0 {
		return ""
	}

	// Words of any letters, not last30days' [a-z0-9]+, so a Vietnamese specialty is one (inferred).
	for _, w := range strings.Fields(normalize(lower)) {
		if !levelAndTeamWords[w] && strings.Trim(w, "0123456789") != "" {
			return tail
		}
	}

	return ""
}
