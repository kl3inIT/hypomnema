package listening

import (
	"context"
	"fmt"
	"slices"
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
// jev-social validateResearchReport). A claim is kept only beside a checked quote.

const (
	// readPrompt is the version of the read's prompt: a cached reading is kept for its version only.
	readPrompt = "read-1"
	// maxQuotes and maxClaims bound what one item gives: jev-social's five excerpts a record (SECURITY.md).
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

// reading is what the model extracted from one item.
type reading struct {
	quotes, claims []string
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
		`characters.
If an item says nothing about the topic, give it no quotes and no claims. Never fill a gap with what the text ` +
		`does not say, and never invent a person, a number or a source.

Return JSON only:
{
  "items": [
    {"id": "id", "quotes": ["verbatim quote"], "claims": ["claim"]}
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

// read reads the targets in full and puts the checked quotes and claims on the items.
func (s *Service) read(ctx context.Context, topic string, items []Item, clusters []Cluster, req Request, c Caller,
	l Limits,
) Step {
	targets := readTargets(clusters, items, l.Read)

	return s.step(ctx, "read", s.readModel, DefaultReadModel, c, func(model string, t *tally, st *Step) error {
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
			if docs[i] != "" {
				asked = append(asked, i)
			}
		}

		if len(asked) > 0 {
			fresh, err := s.askReadings(ctx, topic, items, asked, docs, model, c, t)
			if err != nil {
				return err
			}

			for _, i := range asked {
				r := fresh[i]
				got[i] = r
				s.readings.put(readKey(topic, items[i], model), r)
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
		if len(notes) > 0 {
			st.Detail += "; " + strings.Join(notes, "; ")
		}

		return nil
	})
}

// readDocuments reads the items in full, each through its source, at once, every site at its own pace; a note says
// what was not read, or read in part.
func (s *Service) readDocuments(ctx context.Context, req Request, items []Item, targets []int) (map[int]string,
	[]string,
) {
	docs := make([]string, len(targets))
	errs := make([]error, len(targets))

	var wg sync.WaitGroup

	for n, i := range targets {
		p := s.platform(items[i].Source)
		if p.Read == nil {
			errs[n] = Fail(CodeBroken, "%s has no full read", items[i].Source)

			continue
		}

		wg.Go(func() {
			text, err := p.Read(ctx, req, items[i])
			docs[n], errs[n] = cut(text, websearch.MaxContent), err
		})
	}

	wg.Wait()

	out := map[int]string{}

	var notes []string

	for n, i := range targets {
		if docs[n] != "" {
			out[i] = docs[n]
		}

		if errs[n] != nil {
			what := "not read"
			if docs[n] != "" {
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

// askReadings asks the model to read the documents and checks its answer.
func (s *Service) askReadings(ctx context.Context, topic string, items []Item, asked []int, docs map[int]string,
	model string, c Caller, t *tally,
) (map[int]reading, error) {
	var b strings.Builder

	for _, i := range asked {
		it := items[i]
		date := "unknown"

		if it.PublishedAt != nil {
			date = it.PublishedAt.UTC().Format(time.DateOnly)
		}

		fmt.Fprintf(&b, "=== item %s\nsource: %s; where: %s; author: %s; date: %s\ntitle: %s\n\n%s\n\n", it.ID, it.Source,
			it.Container, it.Author, date, it.Title, docs[i])
	}

	body, err := chatRequest(model, readSystem(topic), fenced("Items", strings.TrimRight(b.String(), "\n")))
	if err != nil {
		return nil, err
	}

	text, err := ask(ctx, c.Model, body, readDeadline, readAttempts, t)
	if err != nil {
		return nil, err
	}

	var answer struct {
		Items []struct {
			ID     string   `json:"id"`
			Quotes []string `json:"quotes"`
			Claims []string `json:"claims"`
		} `json:"items"`
	}

	if err := jsonObject(text, &answer); err != nil {
		return nil, err
	}

	byID := map[string]int{}
	for _, i := range asked {
		byID[items[i].ID] = i
	}

	out := map[int]reading{}
	for _, i := range asked {
		out[i] = reading{}
	}

	for _, row := range answer.Items {
		i, ok := byID[strings.TrimSpace(row.ID)]
		if !ok {
			continue
		}

		r := out[i]
		doc := evened(docs[i])

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
				if claim = clip(claim, maxClaim); claim != "" && len(r.claims) < maxClaims {
					r.claims = append(r.claims, claim)
				}
			}
		}

		out[i] = r
	}

	return out, nil
}

// evened is text as a quote is looked for in it: NFKC, its spaces made one.
func evened(text string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(text)), " ")
}
