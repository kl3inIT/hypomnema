package listening

import (
	"context"
	"fmt"
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
// model read on the page, kept only when the words it quotes for it are on the page and hold its year and day. That
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

func readKey(topic string, it Item, model string) string {
	return cacheKey("read", it.ID, it.Title, it.Text, topic, readPrompt, model)
}

// readResult is what the read changed beyond the items' quotes: the items its pages date outside the window, and
// whether any item's date or directness changed, so the scores and clusters are made again.
type readResult struct {
	outside map[int]bool
	changed bool
}

// read reads the targets in full and puts the checked quotes, claims and page dates on the items.
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

		got := map[int]reading{}

		var fetch []int

		for _, i := range targets {
			if r, ok := s.readings.get(readKey(topic, items[i], model)); ok {
				got[i] = r

				continue
			}

			fetch = append(fetch, i)
		}

		st.Cached = len(got)
		docs, notes := s.readDocuments(ctx, req, items, fetch)

		var asked []int

		for _, i := range fetch {
			if docs[i].Text != "" {
				asked = append(asked, i)
			}
		}

		// The page's own date is code's to read, so it is applied even when the model fails below.
		dated := map[int]*time.Time{}
		for _, i := range asked {
			if docs[i].Published != nil {
				dated[i] = docs[i].Published
			}
		}

		defer func() {
			for i, r := range got {
				if r.published != nil {
					dated[i] = r.published
				}
			}

			notes = append(notes, s.dateByPage(items, dated, req, &res)...)
			st.OutsideWindow = len(res.outside)

			if len(notes) > 0 {
				st.Detail = joinReason(st.Detail, strings.Join(notes, "; "))
			}
		}()

		for _, i := range asked {
			items[i].Direct, res.changed = true, true
		}

		for i := range got {
			items[i].Direct, res.changed = true, true
		}

		if len(asked) > 0 {
			fresh, answered, err := s.askReadings(ctx, topic, items, asked, docs, model, c, t)
			if err != nil {
				return err
			}

			// A judgment the model did not give is not kept: the next search asks for it again (OpinionLens
			// report_sentiment.py:1-14, a missing result falls back and is not cached).
			for _, i := range asked {
				got[i] = fresh[i]
				if answered[i] {
					s.readings.put(readKey(topic, items[i], model), fresh[i])
				}
			}
		}

		if len(got) == 0 {
			return fmt.Errorf("no item could be read in full: %s", strings.Join(notes, "; "))
		}

		quoted := 0

		for i, r := range got {
			items[i].Quotes, items[i].Claims = r.quotes, r.claims
			if len(r.quotes) > 0 {
				quoted++
			}
		}

		st.Detail = fmt.Sprintf("%d of %d representatives read in full, %d with checked quotes", len(got),
			len(targets), quoted)

		return nil
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

// readAnswer is the read's schema: an answer that does not fit it is refused whole (OpinionLens
// report_pipeline/schema_validator.py: types and required fields checked; report_manager.py:15-17, an output that
// fails degrades to the deterministic report).
type readAnswer struct {
	Items []struct {
		ID        string   `json:"id"`
		Quotes    []string `json:"quotes"`
		Claims    []string `json:"claims"`
		Published *struct {
			Date  string `json:"date"`
			Quote string `json:"quote"`
		} `json:"published"`
	} `json:"items"`
}

func (a readAnswer) check() error {
	for n, row := range a.Items {
		if strings.TrimSpace(row.ID) == "" {
			return fmt.Errorf("items[%d] has no id", n)
		}
	}

	return nil
}

// askReadings asks the model to read the documents and checks its answer; answered are the items it judged.
func (s *Service) askReadings(ctx context.Context, topic string, items []Item, asked []int, docs map[int]Document,
	model string, c Caller, t *tally,
) (map[int]reading, map[int]bool, error) {
	var b strings.Builder

	for _, i := range asked {
		it := items[i]
		date := "unknown"

		if it.PublishedAt != nil {
			date = it.PublishedAt.UTC().Format(time.DateOnly)
		}

		fmt.Fprintf(&b, "=== item %s\nsource: %s; where: %s; author: %s; search date: %s\ntitle: %s\n\n%s\n\n", it.ID,
			it.Source, it.Container, it.Author, date, it.Title, docs[i].Text)
	}

	body, err := chatRequest(model, readSystem(topic), fenced("Items", strings.TrimRight(b.String(), "\n")))
	if err != nil {
		return nil, nil, err
	}

	text, err := ask(ctx, c.Model, body, readDeadline, readAttempts, t)
	if err != nil {
		return nil, nil, err
	}

	var answer readAnswer
	if err := decodeAnswer(text, &answer); err != nil {
		return nil, nil, err
	}

	byID := map[string]int{}
	for _, i := range asked {
		byID[items[i].ID] = i
	}

	out := map[int]reading{}
	answered := map[int]bool{}

	for _, i := range asked {
		out[i] = reading{published: docs[i].Published}
	}

	for _, row := range answer.Items {
		i, ok := byID[strings.TrimSpace(row.ID)]
		if !ok {
			continue
		}

		answered[i] = true
		r := out[i]
		doc := evened(docs[i].Text)

		for _, q := range row.Quotes {
			q = strings.Trim(strings.TrimSpace(q), "\"“”'‘’")
			if q == "" || len([]rune(q)) > maxQuote || len(r.quotes) == maxQuotes || slices.Contains(r.quotes, q) ||
				!strings.Contains(doc, evened(q)) {
				continue
			}

			r.quotes = append(r.quotes, q)
		}

		if len(r.quotes) > 0 {
			for _, claim := range row.Claims {
				claim = clip(claim, maxClaim)
				if claim != "" && len(r.claims) < maxClaims && numberNotIn(claim, docs[i].Text) == "" {
					r.claims = append(r.claims, claim)
				}
			}
		}

		if r.published == nil && row.Published != nil {
			r.published = pageDate(row.Published.Date, row.Published.Quote, doc)
		}

		out[i] = r
	}

	return out, answered, nil
}

// pageDate is the date the model read on the page, when the words it quotes for it are on the page and hold the
// date's year and its day; nil otherwise.
func pageDate(date, quote, doc string) *time.Time {
	at, err := time.Parse(time.DateOnly, strings.TrimSpace(date))
	if err != nil {
		return nil
	}

	quote = evened(quote)
	if quote == "" || !strings.Contains(doc, quote) {
		return nil
	}

	have := map[string]bool{}
	for _, n := range numbers.FindAllString(quote, -1) {
		have[strings.TrimLeft(n, "0")] = true
	}

	if !have[strconv.Itoa(at.Year())] || !have[strconv.Itoa(at.Day())] {
		return nil
	}

	return &at
}

// evened is text as a quote is looked for in it: NFKC, its spaces made one.
func evened(text string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(text)), " ")
}
