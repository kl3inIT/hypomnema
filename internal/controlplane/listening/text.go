package listening

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Text as this package compares it. Vietnamese is written with combining marks that a page may send composed or
// decomposed, so text is put in NFKC and case-folded first, as obsei normalizes a record's text
// (core/record.py:22-23, llm/embed.py:41). A word is a run of letters, marks and digits: Go's \w, like last30days'
// ASCII-only tokenizers, would cut "đăng" into pieces. No reference handles Vietnamese (research note §3); the test
// in cluster_test.go is what chose the comparison.

const (
	// maxTextRunes cuts an item's text: last30days keeps a Reddit post's first 400 characters as its snippet
	// (normalize.py:399-400).
	maxTextRunes  = 400
	maxTitleRunes = 200
)

// stopwords are last30days' (lib/dedupe.py:9-35), English only: no reference has a Vietnamese list, and the
// Vietnamese test needed none.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "to": true, "for": true, "how": true, "is": true, "in": true, "of": true,
	"on": true, "and": true, "with": true, "from": true, "by": true, "at": true, "this": true, "that": true,
	"it": true, "what": true, "are": true, "do": true, "can": true,
}

// normalize is text in NFKC, case-folded, with every run that is not a word made one space.
func normalize(text string) string {
	text = strings.ToLower(norm.NFKC.String(text))

	var b strings.Builder

	space := true

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) {
			b.WriteRune(r)

			space = false

			continue
		}

		if !space {
			b.WriteByte(' ')

			space = true
		}
	}

	return strings.TrimSpace(b.String())
}

// words are the normalized text's words of two letters or more that are not stopwords (last30days
// dedupe._tokenize: len > 1, not a stopword).
func words(normalized string) []string {
	var out []string

	for w := range strings.FieldsSeq(normalized) {
		if len([]rune(w)) > 1 && !stopwords[w] {
			out = append(out, w)
		}
	}

	return out
}

func set(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, it := range items {
		out[it] = true
	}

	return out
}

// trigrams are the normalized text's character 3-grams (last30days dedupe._ngrams_of_normalized).
func trigrams(normalized string) map[string]bool {
	r := []rune(normalized)
	if len(r) < 3 {
		if len(r) == 0 {
			return map[string]bool{}
		}

		return map[string]bool{normalized: true}
	}

	out := make(map[string]bool, len(r))
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = true
	}

	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	both := 0

	for k := range a {
		if b[k] {
			both++
		}
	}

	return float64(both) / float64(len(a)+len(b)-both)
}

// prepared is a text's trigrams and words, for repeated comparisons (last30days dedupe._PreparedText).
type prepared struct {
	grams map[string]bool
	words map[string]bool
}

func prepare(text string) prepared {
	n := normalize(text)

	return prepared{grams: trigrams(n), words: set(words(n))}
}

// similarity is the larger of the trigram and the word Jaccard (last30days dedupe.prepared_similarity).
func (p prepared) similarity(o prepared) float64 {
	return math.Max(jaccard(p.grams, o.grams), jaccard(p.words, o.words))
}

// Similarity is how alike two texts are, from 0 to 1. With a topic, the topic's words are taken out of both first and
// texts left with fewer than two words are not alike, since every item names the topic: last30days-cn
// dedupe.event_similarity and strip_query_terms (lib/dedupe.py:137-166).
func Similarity(a, b, topic string) float64 {
	if topic != "" {
		a, b = withoutTopic(a, topic), withoutTopic(b, topic)
		if len(words(a)) < 2 || len(words(b)) < 2 {
			return 0
		}
	}

	return prepare(a).similarity(prepare(b))
}

// withoutTopic is the normalized text without the topic's phrase and words.
func withoutTopic(text, topic string) string {
	text, topic = normalize(text), normalize(topic)
	if topic == "" {
		return text
	}

	text = strings.ReplaceAll(" "+text+" ", " "+topic+" ", " ")
	drop := set(strings.Fields(topic))

	var kept []string

	for w := range strings.FieldsSeq(text) {
		if !drop[w] {
			kept = append(kept, w)
		}
	}

	return strings.Join(kept, " ")
}

// relevance is how much of a query a text holds, from 0 to 1: last30days token_overlap_relevance
// (lib/relevance.py:137-218), its coverage, precision and phrase bonus. Its informative-token term counts every
// word here, there being no stopword list for Vietnamese.
func relevance(query, text string) float64 {
	nq, nt := normalize(query), normalize(text)
	q, t := set(words(nq)), set(words(nt))

	if len(q) == 0 {
		return 0.5
	}

	overlap := 0

	for w := range q {
		if t[w] {
			overlap++
		}
	}

	if overlap == 0 {
		return 0
	}

	coverage := float64(overlap) / float64(len(q))
	precision := float64(overlap) / float64(max(1, min(len(t), len(q)+4)))

	bonus := 0.0
	if nq != "" && strings.Contains(" "+nt+" ", " "+nq+" ") {
		bonus = 0.16
		if strings.Contains(nq, " ") {
			bonus = 0.12
		}
	}

	return min(1, 0.55*math.Pow(coverage, 1.35)+0.25*coverage+0.20*precision+bonus)
}

// NormalizeURL is an address as duplicates share it: scheme and host lowercased, "www.", "old." and "m." left off,
// the fragment and utm_* parameters dropped, the other parameters sorted, the path's last "/" dropped (last30days
// fusion._normalize_url, lib/fusion.py:35-63). A Reddit thread is its thread's key, whatever form its address takes
// (last30days reddit_refs.thread_key).
func NormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if key := redditThreadKey(raw); key != "" {
		return key
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}

	host := strings.ToLower(u.Host)

	for {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(host, "www."), "old."), "m.")
		if trimmed == host {
			break
		}

		host = trimmed
	}

	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") {
			q.Del(k)
		}
	}

	out := url.URL{
		Scheme: strings.ToLower(u.Scheme), Host: host, Path: strings.TrimRight(u.EscapedPath(), "/"),
		RawQuery: q.Encode(),
	}

	s, _ := url.PathUnescape(out.String())

	return s
}

// The hosts and forms of a Reddit thread's address (last30days reddit_refs.canonical_thread, lib/reddit_refs.py).
var (
	redditHosts = []string{
		"reddit.com", "www.reddit.com", "old.reddit.com", "new.reddit.com", "np.reddit.com", "m.reddit.com",
		"sh.reddit.com",
	}
	redditShortHosts = []string{"redd.it", "www.redd.it"}
	redditID         = regexp.MustCompile(`^[0-9a-z]{1,13}$`)
	subredditName    = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)
)

// redditThread is a Reddit thread address's post id and subreddit; ok is false for another address, a share link
// or a profile.
func redditThread(raw string) (id, subreddit string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", "", false
	}

	host := strings.ToLower(u.Hostname())

	var segments []string

	for s := range strings.SplitSeq(u.Path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}

	if slices.Contains(redditShortHosts, host) {
		if len(segments) == 1 && redditID.MatchString(strings.ToLower(segments[0])) {
			return strings.ToLower(segments[0]), "", true
		}

		return "", "", false
	}

	if !slices.Contains(redditHosts, host) {
		return "", "", false
	}

	if len(segments) >= 2 && strings.EqualFold(segments[0], "r") {
		if !subredditName.MatchString(segments[1]) {
			return "", "", false
		}

		subreddit = segments[1]
		segments = segments[2:]
	}

	if len(segments) < 2 || !strings.EqualFold(segments[0], "comments") {
		return "", "", false
	}

	id = strings.TrimPrefix(strings.ToLower(segments[1]), "t3_")
	if !redditID.MatchString(id) {
		return "", "", false
	}

	return id, subreddit, true
}

// redditThreadKey is the key every address of one Reddit thread shares; empty for another address.
func redditThreadKey(raw string) string {
	id, _, ok := redditThread(raw)
	if !ok {
		return ""
	}

	return "https://reddit.com/comments/" + id
}

// itemID is an item's stable id: a hash of its source and native id (obsei Record.make_id, core/record.py:74-76).
func itemID(source, nativeID string) string {
	sum := sha256.Sum256([]byte(source + "\x1f" + nativeID))

	return "it_" + hex.EncodeToString(sum[:8])
}

// clip cuts text to n characters on one line.
func clip(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")

	r := []rune(text)
	if len(r) <= n {
		return text
	}

	return string(r[:n]) + "…"
}
