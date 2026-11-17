package meetings

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
)

// The names voices give themselves, offered for the owner to accept: MemoryOS's SpeakerIntroductions
// (memoryos-core/src/main/java/io/memoryos/meeting/SpeakerIntroductions.java). Meetings open with people saying who
// they are, "Chào mọi người, mình là Minh"; the sentence is enough to offer the name with the line it came from.
// Nothing here renames anybody and no model is asked.

// Suggestion is a name offered for an unnamed voice, from the line it said it in.
type Suggestion struct {
	Label string
	Name  string
	Line  int
}

const (
	// name is one to four capitalized words; longer is a sentence that happens to start with one.
	name = `(\p{Lu}\p{L}*(?:\s+\p{Lu}\p{L}*){0,3})`
	// self are the words a speaker calls themselves by, read whatever their case.
	self = `(?i:tôi|mình|em|anh|chị|tớ|con|cháu)`
)

var (
	introductions = []*regexp.Regexp{
		// "tôi là Minh", "mình tên là Anh Minh", "em tên Lan"
		regexp.MustCompile(`(?:^|[^\p{L}])` + self + `\s+(?i:tên\s+)?(?i:là\s+)?` + name),
		// "tên tôi là Minh", "tên mình Minh"
		regexp.MustCompile(`(?:^|[^\p{L}])(?i:tên)\s+` + self + `\s+(?i:là\s+)?` + name),
		regexp.MustCompile(`(?:^|[^\p{L}])(?i:I\s+am|I'm|my\s+name\s+is|this\s+is)\s+` + name),
	}
	// announcement is "Minh đây", "Minh xin phép": taken for a name only when the meeting expects somebody called that.
	announcement = regexp.MustCompile(`^` + name + `\s+(?i:đây|xin\s+phép)(?:$|[^\p{L}])`)
	sentences    = regexp.MustCompile(`[.!?]\s+`)
)

// notAName are words that follow "tôi là" without being a name, written without marks.
var notAName = map[string]bool{}

func init() {
	for _, w := range strings.Fields("nguoi ai gi mot nhan vien truong phong giam doc ban be sinh hoc ke toan " +
		"thu ky chu tri khach moi day nay the cua va co khong duoc cong ty bo phan dai dien nhom xin chao vang " +
		"roi thi se dang phai can muon biet nghi thay lam") {
		notAName[w] = true
	}
}

// suggest returns, for each unnamed voice, the name it called itself at the earliest line where it did.
func suggest(lines []meetingstore.Line, speakers []meetingstore.Speaker, people []string) []Suggestion {
	unnamed := map[string]bool{}

	for _, s := range speakers {
		if s.Name == "" {
			unnamed[s.Label] = true
		}
	}

	expected := make([]string, 0, len(people))
	for _, p := range people {
		if p = plain(p); p != "" {
			expected = append(expected, p)
		}
	}

	var out []Suggestion

	found := map[string]bool{}

	for _, l := range lines {
		if !unnamed[l.Speaker] || found[l.Speaker] {
			continue
		}

		if n, ok := said(l.Text, expected); ok {
			found[l.Speaker] = true
			out = append(out, Suggestion{Label: l.Speaker, Name: n, Line: l.N})
		}
	}

	return out
}

func said(text string, expected []string) (string, bool) {
	for _, sentence := range sentences.Split(text, -1) {
		sentence = strings.TrimSpace(sentence)

		for _, p := range introductions {
			if m := p.FindStringSubmatch(sentence); m != nil {
				if n := trimName(m[1]); n != "" {
					return n, true
				}
			}
		}

		if m := announcement.FindStringSubmatch(sentence); m != nil {
			if n := trimName(m[1]); n != "" && awaited(expected, plain(n)) {
				return n, true
			}
		}
	}

	return "", false
}

// awaited says a participant goes by this name: the whole of it, or the given name a Vietnamese full name ends with.
func awaited(expected []string, n string) bool {
	for _, p := range expected {
		if p == n || strings.HasSuffix(p, " "+n) {
			return true
		}
	}

	return false
}

// trimName keeps the leading words that can be a name and drops the rest of the sentence the pattern swept up.
func trimName(candidate string) string {
	var kept []string

	for _, w := range strings.Fields(candidate) {
		if notAName[plain(w)] {
			break
		}

		kept = append(kept, w)
	}

	return strings.Join(kept, " ")
}

// plain is lower case without marks, so "Minh" answers to "minh" and a list of people written plainly still matches.
func plain(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

	out, _, err := transform.String(t, strings.TrimSpace(s))
	if err != nil {
		out = s
	}

	return strings.ToLower(strings.NewReplacer("đ", "d", "Đ", "D").Replace(out))
}
