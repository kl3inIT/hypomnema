package listening

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The full read (layer 2b): after the ranking and the clusters, the representatives of the clusters, the best first,
// are read in full, the post and its top comments, and one call to a model with a long context extracts from each
// the verbatim quotes and the claims the report will cite. gpt-researcher kept full pages after cutting them to about
// 1k tokens cost answer quality (#2057), and last30days fetches what it reads in full for the final shortlist only,
// after ranking (#542). A quote counts only when it is in what was read, character for character once spaces and
// Unicode forms are evened out: an item found by a model is checked against its page before it counts (research
// note §2, last30days grok_x _validate_items), and an answer may cite only what was given (obsei ask.py:151-152,
// jev-social validateResearchReport). A claim is kept only beside a checked quote, and only when every number in it
// is one the post itself has: code owns the numbers, the model the words (OpinionLens
// report_pipeline/narrator.py:9-11).
//
// The read also dates each item by its own page (owner, 2026-11-28; no reference checks a date on the page: a
// snippet-dated 2019 page came back stamped 2026 on production): the date the page's metadata gives, read by code
// (websearch.Page.Published, Agent-Reach's "Published Time:", agent_reach/channels/web.py:141-150), else the date the
// model read on the page, kept only when the words it quotes for it are on the page and write that very date. That
// date replaces a snippet's that disagrees, and an item its page dates outside the window is dropped and counted
// (trend-opportunity-radar counts content publication time, never capture time, references/scoring-contract.md:16).

const (
	// readPrompt is the version of the read's prompt: a cached reading is kept for its version only.
	readPrompt = "read-2"
	// maxQuotes and maxClaims bound what one item gives: jev-social's five excerpts a record (SECURITY.md:24).
	maxQuotes = 5
	maxClaims = 5
	// maxQuote is the longest quote: last30days' stored quote (snippet.QUOTE_LIMIT, 400); maxClaim a claim, as long
	// as jev-social's comment excerpt (300).
	maxQuote = 400
	maxClaim = 300
	// readComments are the top comments a thread is read with (last30days reddit_arctic.fetch_post_comments,
	// limit 10), from a tree of at most arcticCommentRows (FALLBACK_COMMENT_ROWS, 500: a 500-row tree kept a busy
	// thread's top replies, #1467).
	readComments      = 10
	arcticCommentRows = 500
)

// reading is what was read of one item: the quotes and claims the model extracted, and the date its page gives.
type reading struct {
	quotes, claims []string
	published      *time.Time
}

// readSystem tells the model what to extract; thin data is reported as thin, never filled (BettaFish #597 invented
// a user, gpt-researcher #1572 sources; obsei ask.py: "If the evidence is thin or missing, say so").
func readSystem(topic string) string {
	return "Read the full posts of a social listening search about " + topic + `, each with its top comments, ` +
		`and extract what a report about the topic would cite.
For each item give up to 5 quotes and up to 5 claims:
- a quote is copied exactly, character for character, from that item's text, at most 400 characters: what the ` +
		`author or a commenter says about the topic, in their own words and language; never translate, fix, join ` +
		`or shorten a quote with an ellipsis. A quote that is not in the item's text is dropped.
- a claim says in one sentence, in the text's language, what the item asserts or complains about, at most 300 ` +
		`characters. Write a number only as the text writes it.
- published: when the text itself says when the post was published, that date as YYYY-MM-DD and, as "quote", the ` +
		`exact words of the text that say it; leave it out when the text does not say, and never give the date of a ` +
		`comment, an edit or today.
If an item says nothing about the topic, give it no quotes and no claims. Never fill a gap with what the text ` +
		`does not say, and never invent a person, a number or a source.

Return JSON only:
{
  "items": [
    {"id": "id", "quotes": ["verbatim quote"], "claims": ["claim"],
     "published": {"date": "YYYY-MM-DD", "quote": "the words that give it"}}
  ]
}`
}

// readTargets are the items read in full: the clusters' representatives, the best cluster first, at most limit.
func readTargets(clusters []Cluster, items []Item, limit int) []int {
	index := map[string]int{}
	for i, it := range items {
		index[it.ID] = i
	}

	var out []int

	for _, cl := range clusters {
		for _, id := range cl.Representatives {
			if len(out) == limit {
				return out
			}

			if i, ok := index[id]; ok && !slices.Contains(out, i) {
				out = append(out, i)
			}
		}
	}

	return out
}

// readKey is a reading's key in the cache: the item and the text the model is given for it, read again each time,
// so a page whose text changed is read again (OpinionLens keys its sentiment cache on the hash of the text it sends,
// app/sidecar/report_sentiment.py:250-262).
func readKey(topic, block, model string) string {
	return cacheKey("read", block, topic, readPrompt, model)
}

// readBlock is all the model is given for one item, the same text the reading is kept by: its id, source, where,
// author, date, title (the post's own when the read gave one) and the text read.
func readBlock(it Item, doc Document) string {
	date := "unknown"
	if it.PublishedAt != nil {
		date = it.PublishedAt.UTC().Format(time.DateOnly)
	}

	title := it.Title
	if doc.Title != "" {
		title = doc.Title
	}

	return fmt.Sprintf("=== item %s\nsource: %s; where: %s; author: %s; search date: %s\ntitle: %s\n\n%s\n\n", it.ID,
		it.Source, it.Container, it.Author, date, title, doc.Text)
}

// readResult is what the read changed beyond the items' quotes: the items its pages date outside the window, and
// whether any item's date or directness changed, so the scores and clusters are made again.
type readResult struct {
	outside map[int]bool
	changed bool
}

// read reads the targets in full and puts the checked quotes, claims and page dates on the items. Only an item whose
// post or page was read is direct and has its quotes checked; one whose read failed stays a search card (radar
// counts a direct source by its captured detail, scripts/audit_clusters.py:63).
func (s *Service) read(ctx context.Context, topic string, items []Item, clusters []Cluster, req Request, c Caller,
	l Limits,
) (Step, readResult) {
	targets := readTargets(clusters, items, l.Read)
	res := readResult{outside: map[int]bool{}}

	st := s.step(ctx, "read", s.readModel, DefaultReadModel, c, func(model string, t *tally, st *Step) error {
		if len(targets) == 0 {
			st.Detail = "nothing to read"

			return nil
		}

		docs, notes := s.readDocuments(ctx, req, items, targets)
		got := map[int]reading{}
		dated := map[int]*time.Time{}

		var asked []int

		for _, i := range targets {
			doc, ok := docs[i]
			if !ok {
				continue
			}

			items[i].Direct, res.changed = true, true

			// The page's own date is code's to read, so it stands even when the model fails below.
			if doc.Published != nil {
				dated[i] = doc.Published
			}

			if r, ok := s.readings.get(readKey(topic, readBlock(items[i], doc), model)); ok {
				got[i] = r

				continue
			}

			asked = append(asked, i)
		}

		st.Cached = len(got)

		defer func() {
			for i, r := range got {
				if dated[i] == nil && r.published != nil {
					dated[i] = r.published
				}
			}

			notes = append(notes, s.dateByPage(items, dated, req, &res)...)
			st.OutsideWindow = len(res.outside)

			if len(notes) > 0 {
				st.Detail = joinReason(st.Detail, strings.Join(notes, "; "))
			}
		}()

		if len(docs) == 0 {
			return fmt.Errorf("no item could be read in full: %s", strings.Join(notes, "; "))
		}

		var (
			refused refusedRows
			askErr  error
		)

		if len(asked) > 0 {
			blocks := map[int]string{}
			for _, i := range asked {
				blocks[i] = readBlock(items[i], docs[i])
			}

			var fresh map[int]reading

			// Only a valid reading is kept; an item the model gave none for falls back to no quotes and is asked
			// again next time (OpinionLens report_sentiment.py:1-14, a missing result is not cached). A failed ask
			// falls back for the items asked only: the kept readings of the others stand.
			fresh, refused, askErr = s.askReadings(ctx, topic, asked, blocks, docs, items, model, c, t)
			for i, reading := range fresh {
				got[i] = reading
				s.readings.put(readKey(topic, blocks[i], model), reading)
			}
		}

		quoted := 0

		for i, r := range got {
			items[i].Quotes, items[i].Claims = r.quotes, r.claims
			if len(r.quotes) > 0 {
				quoted++
			}
		}

		st.Detail = joinReason(fmt.Sprintf("%d of %d representatives read in full, %d judged by the model, %d with "+
			"checked quotes", len(docs), len(targets), len(got), quoted), refused.note())

		return askErr
	})

	return st, res
}

// dateByPage puts each page's date on its item where the item's date disagrees by day, and marks the items it dates
// outside the window; it answers a note for each.
func (s *Service) dateByPage(items []Item, dated map[int]*time.Time, req Request, res *readResult) []string {
	var notes []string

	for i, at := range dated {
		it := &items[i]
		day := at.UTC().Format(time.DateOnly)

		if it.PublishedAt == nil || it.PublishedAt.UTC().Format(time.DateOnly) != day {
			was := "undated"
			if it.PublishedAt != nil {
				was = "dated " + it.PublishedAt.UTC().Format(time.DateOnly)
			}

			notes = append(notes, fmt.Sprintf("%s dated %s by its page (%s before)", it.ID, day, was))
			it.PublishedAt, it.DateConfidence, res.changed = at, "high", true
		}

		if !inDays(at, req.From, req.To) {
			res.outside[i] = true
			notes = append(notes, fmt.Sprintf("%s dropped: its page dates it %s, outside the window", it.ID, day))
		}
	}

	slices.Sort(notes)

	return notes
}

// readDocuments reads the items in full, each through its source, at once, every site at its own pace; a note says
// what was not read, or read in part.
func (s *Service) readDocuments(ctx context.Context, req Request, items []Item, targets []int) (map[int]Document,
	[]string,
) {
	docs := make([]Document, len(targets))
	errs := make([]error, len(targets))

	var wg sync.WaitGroup

	for n, i := range targets {
		p := s.platform(items[i].Source)
		if p.Read == nil {
			errs[n] = Fail(CodeBroken, "%s has no full read", items[i].Source)

			continue
		}

		wg.Go(func() {
			doc, err := p.Read(ctx, req, items[i])
			doc.Text = cut(doc.Text, websearch.MaxContent)
			docs[n], errs[n] = doc, err
		})
	}

	wg.Wait()

	out := map[int]Document{}

	var notes []string

	for n, i := range targets {
		if docs[n].Text != "" {
			out[i] = docs[n]
		}

		if errs[n] != nil {
			what := "not read"
			if docs[n].Text != "" {
				what = "read in part"
			}

			notes = append(notes, fmt.Sprintf("%s %s: %s", items[i].ID, what, oneLine(AsFailure(errs[n]).Error())))
		}
	}

	return out, notes
}

// cut cuts text at n characters, keeping its lines.
func cut(text string, n int) string {
	r := []rune(strings.TrimSpace(text))
	if len(r) <= n {
		return string(r)
	}

	return string(r[:n])
}

// readRow is a reading's schema: the item's id, its quotes and its claims, both arrays and either empty when the item
// says nothing on the topic, and the date the text gives, when it gives one.
type readRow struct {
	ID        string    `json:"id"`
	Quotes    *[]string `json:"quotes"`
	Claims    *[]string `json:"claims"`
	Published *struct {
		Date  string `json:"date"`
		Quote string `json:"quote"`
	} `json:"published"`
}

func (r readRow) check() error {
	switch {
	case strings.TrimSpace(r.ID) == "":
		return errors.New("has no id")
	case r.Quotes == nil:
		return errors.New("has no quotes array")
	case r.Claims == nil:
		return errors.New("has no claims array")
	}

	return nil
}

// askReadings asks the model to read the documents and checks its answer, row by row: the readings it validly gave,
// by item, and the rows refused. No valid reading at all is the step's failure, with its reason.
func (s *Service) askReadings(ctx context.Context, topic string, asked []int, blocks map[int]string,
	docs map[int]Document, items []Item, model string, c Caller, t *tally,
) (map[int]reading, refusedRows, error) {
	var (
		b       strings.Builder
		refused refusedRows
	)

	for _, i := range asked {
		b.WriteString(blocks[i])
	}

	body, err := chatRequest(model, readSystem(topic), fenced("Items", strings.TrimRight(b.String(), "\n")))
	if err != nil {
		return nil, refused, err
	}

	text, err := ask(ctx, c.Model, body, readDeadline, readAttempts, t)
	if err != nil {
		return nil, refused, err
	}

	rows, err := answerRows(text, "items")
	if err != nil {
		return nil, refused, err
	}

	byID := map[string]int{}
	for _, i := range asked {
		byID[items[i].ID] = i
	}

	out := map[int]reading{}

	for n, raw := range rows {
		var row readRow

		if err := decodeRow(raw, &row); err != nil {
			refused.add("items", n, err)

			continue
		}

		if err := row.check(); err != nil {
			refused.add("items", n, err)

			continue
		}

		i, ok := byID[strings.TrimSpace(row.ID)]
		if !ok {
			continue
		}

		doc := evened(docs[i].Text)

		var r reading

		for _, q := range *row.Quotes {
			q = strings.Trim(strings.TrimSpace(q), "\"“”'‘’")
			if q == "" || len([]rune(q)) > maxQuote || len(r.quotes) == maxQuotes || slices.Contains(r.quotes, q) ||
				!strings.Contains(doc, evened(q)) {
				continue
			}

			r.quotes = append(r.quotes, q)
		}

		if len(r.quotes) > 0 {
			for _, claim := range *row.Claims {
				claim = clip(claim, maxClaim)
				if claim != "" && len(r.claims) < maxClaims && numberNotIn(claim, docs[i].Text) == "" {
					r.claims = append(r.claims, claim)
				}
			}
		}

		if row.Published != nil {
			r.published = pageDate(row.Published.Date, row.Published.Quote, doc)
		}

		out[i] = r
	}

	if len(out) == 0 {
		why := fmt.Sprintf("it answered none of the %d items it was given", len(asked))
		if refused.n > 0 {
			why = refused.note()
		}

		return nil, refused, fmt.Errorf("the model gave no valid reading: %s", why)
	}

	return out, refused, nil
}

// The forms a date is written in: day/month/year, as Vietnamese pages write it, year-month-day, the Vietnamese
// "(ngày) D tháng M (năm) Y" with the month in digits or in words ("tháng mười một"), and the English month's name
// before or after the day ("November 22, 2019", "Nov. 22, 2019", "22 November 2019"). Every number is read whole:
// what precedes and follows it is not a digit, so "20190" is no 2019.
var (
	dayMonthYear = regexp.MustCompile(`(?:^|\D)(\d{1,2})[/.-](\d{1,2})[/.-](\d{4})(?:\D|$)`)
	yearMonthDay = regexp.MustCompile(`(?:^|\D)(\d{4})[/.-](\d{1,2})[/.-](\d{1,2})(?:\D|$)`)
	vietnamese   = regexp.MustCompile(`(?i)(?:^|\D)(\d{1,2})\s+tháng\s+(\d{1,2}|` + vietnameseMonth +
		`)[\s,]+(?:năm\s+)?(\d{4})(?:\D|$)`)
	monthName = `(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|` +
		`oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?`
	monthDayYear = regexp.MustCompile(`(?i)\b` + monthName + `\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(\d{4})(?:\D|$)`)
	dayMonthName = regexp.MustCompile(`(?i)(?:^|\D)(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?` + monthName +
		`,?\s+(\d{4})(?:\D|$)`)
)

// vietnameseMonths are the Vietnamese months in words, by number; the longer names first, so "mười một" is not read
// as "mười". "Giêng" and "chạp" are the lunar calendar's first and last months, which a page's date does not use.
var vietnameseMonths = []struct {
	name   string
	number int
}{
	{"mười một", 11}, {"mười hai", 12}, {"mười", 10}, {"một", 1}, {"hai", 2}, {"ba", 3}, {"tư", 4}, {"bốn", 4},
	{"năm", 5}, {"sáu", 6}, {"bảy", 7}, {"tám", 8}, {"chín", 9},
}

var vietnameseMonth = func() string {
	names := make([]string, 0, len(vietnameseMonths))
	for _, m := range vietnameseMonths {
		names = append(names, strings.ReplaceAll(m.name, " ", `\s+`))
	}

	return strings.Join(names, "|")
}()

// monthOf is a month's number from its digits, its English name or first three letters, or its Vietnamese words.
func monthOf(name string) string {
	name = strings.ToLower(strings.Join(strings.Fields(name), " "))
	if _, err := strconv.Atoi(name); err == nil {
		return name
	}

	for _, m := range vietnameseMonths {
		if name == m.name {
			return strconv.Itoa(m.number)
		}
	}

	for n, m := range []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"} {
		if strings.HasPrefix(name, m) {
			return strconv.Itoa(n + 1)
		}
	}

	return ""
}

// pageDate is the date the model read on the page: taken only when the words it quotes for it are on the page and
// write that very date, day, month and year (owner, 2026-11-28; no reference checks a date on the page); nil
// otherwise.
func pageDate(date, quote, doc string) *time.Time {
	at, err := time.Parse(time.DateOnly, strings.TrimSpace(date))
	if err != nil {
		return nil
	}

	quote = evened(quote)
	if quote == "" || !strings.Contains(doc, quote) {
		return nil
	}

	matches := func(y, m, d string) bool {
		year, _ := strconv.Atoi(y)
		month, _ := strconv.Atoi(m)
		day, _ := strconv.Atoi(d)

		return year == at.Year() && month == int(at.Month()) && day == at.Day()
	}

	for _, f := range dayMonthYear.FindAllStringSubmatch(quote, -1) {
		if matches(f[3], f[2], f[1]) {
			return &at
		}
	}

	for _, f := range yearMonthDay.FindAllStringSubmatch(quote, -1) {
		if matches(f[1], f[2], f[3]) {
			return &at
		}
	}

	for _, f := range vietnamese.FindAllStringSubmatch(quote, -1) {
		if matches(f[3], monthOf(f[2]), f[1]) {
			return &at
		}
	}

	for _, f := range monthDayYear.FindAllStringSubmatch(quote, -1) {
		if matches(f[3], monthOf(f[1]), f[2]) {
			return &at
		}
	}

	for _, f := range dayMonthName.FindAllStringSubmatch(quote, -1) {
		if matches(f[3], monthOf(f[2]), f[1]) {
			return &at
		}
	}

	return nil
}

// evened is text as a quote is looked for in it: NFKC, its spaces made one.
func evened(text string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(text)), " ")
}
