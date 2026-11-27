package listening

import (
	"context"
	"log/slog"
	"slices"
	"time"
)

// Backend is one way of reading a source: Agent-Reach's (backend label, method) of an Action
// (channels/base.py:30-50), last30days' BackendSpec (lib/backends.py:118-132).
type Backend struct {
	Name string
	// Host is the site or account its calls go to. One call at a time goes to a host (pace.go), and a refusal, a
	// rate limit, a spent quota or a sign-in wall stops the backends of the same host for this search, as
	// Agent-Reach's final codes stop a channel's other backends (runtime/result.py:33), while a backend on another
	// host is still asked, as last30days asks Reddit's other paths after one is blocked (reddit_keyless.py:34-39).
	Host string
	// OptIn: never asked unless the plan pins the source to it (last30days opt_in, lib/backends.py:122-124).
	OptIn bool
	// Probe says, without a call and without a side effect, why the backend cannot be asked now; nil when it can
	// (last30days: "probe must be side-effect-free", lib/backends.py:118-125).
	Probe func(r Request) *SourceError
	// Search reads one subquery; an error is reported by its code (AsFailure).
	Search func(ctx context.Context, r Request) (Result, error)
}

// Result is what a backend read: the items, and a note on how (which provider answered, which step failed).
type Result struct {
	Items  []Item
	Detail string
}

// Platform is a source and its backends, in the order they are tried (Agent-Reach Action.backends).
type Platform struct {
	Name     string
	Backends []Backend
}

func (p Platform) backendNames() []string {
	out := make([]string, 0, len(p.Backends))
	for _, b := range p.Backends {
		out = append(out, b.Name)
	}

	return out
}

// Request is what a backend is asked.
type Request struct {
	Query string
	Limit int
	// From and To are the window: an item dated outside it is dropped.
	From, To time.Time
	// Web asks the web search chain; nil: it cannot be asked.
	Web WebSearch
	// Pace waits for the backend's turn at a host, at least gap after the last call there, and gives the function
	// that ends its turn.
	Pace func(ctx context.Context, host string, gap time.Duration) (func(), error)
}

// Attempt is one backend tried for a stream, as Agent-Reach's meta.attempts (runtime/dispatch.py:255-290): its
// result, "ok" or a failure code, what happened, and until when it rests.
type Attempt struct {
	Backend string `json:"backend"`
	Result  string `json:"result"`
	Detail  string `json:"detail,omitempty"`
	// Until is when it may be asked again, after a refusal or a rate limit.
	Until string `json:"until,omitempty"`
	// Skipped: not asked this time, because it rests, its probe failed or its site already refused.
	Skipped bool `json:"skipped,omitempty"`
}

const resultOK = "ok"

// stream is one (subquery, source) stream: what it reported, and its items in the backend's order.
type stream struct {
	report   Stream
	subquery Subquery
	source   string
	items    []Item
}

// stopsHost are the codes after which the other backends of the same host are not asked in this search: Agent-Reach
// FINAL_CODES (runtime/result.py:33) less not_found and bad input, which are about the question, not the site.
func stopsHost(c Code) bool {
	return c == CodeNeedLogin || c == CodeRefused || c == CodeRateLimited || c == CodeLimitReached
}

// runStream reads one subquery from one source: its backends in order, the first that reads it serving
// (collect-then-pick, last30days _resolve_alternative, lib/backends.py:835-855; Agent-Reach run_action,
// dispatch.py:276-322). A backend that reads nothing in the window is not an answer: the next is asked, as the web
// search asks the next provider (websearch.search) and last30days backfills a thin Reddit (pipeline.py).
func (s *Service) runStream(ctx context.Context, p Platform, pin string, sq Subquery, req Request) stream {
	st := stream{subquery: sq, source: p.Name, report: Stream{Subquery: sq.Label}}

	var (
		stopped = map[string]Code{}
		clean   bool
		failed  []Code
	)

	for _, b := range p.Backends {
		if pin != "" && b.Name != pin || pin == "" && b.OptIn {
			continue
		}

		id := p.Name + "/" + b.Name
		skip := func(f *SourceError) {
			a := Attempt{Backend: b.Name, Result: string(f.Code), Detail: f.Detail, Skipped: true}
			if !f.Until.IsZero() {
				a.Until = f.Until.UTC().Format(time.RFC3339)
			}

			st.report.Tried = append(st.report.Tried, a)
			failed = append(failed, f.Code)
		}

		if code, ok := stopped[b.Host]; ok {
			skip(&SourceError{Code: code, Detail: "its site refused this search a moment ago"})

			continue
		}

		if rest := s.restOf(id); rest != nil {
			skip(rest)

			continue
		}

		if b.Probe != nil {
			if f := b.Probe(req); f != nil {
				skip(f)

				continue
			}
		}

		res, err := b.Search(ctx, req)
		if err != nil {
			f := AsFailure(err)
			if ctx.Err() != nil {
				f = Fail(CodeTimeout, "the search was stopped before this source answered")
			}

			until := s.rest(id, f)

			a := Attempt{Backend: b.Name, Result: string(f.Code), Detail: f.Detail}
			if !until.IsZero() {
				a.Until = until.UTC().Format(time.RFC3339)
			}

			st.report.Tried = append(st.report.Tried, a)
			failed = append(failed, f.Code)

			s.logger.WarnContext(ctx, "a listening backend failed", slog.String("event", "listening.backend_failed"),
				slog.String("backend", id), slog.String("code", string(f.Code)), slog.String("detail", f.Detail))

			if stopsHost(f.Code) {
				stopped[b.Host] = f.Code
			}

			if f.Code == CodeNotFound {
				break
			}

			continue
		}

		items := s.inWindow(res.Items, p.Name, b.Name, sq.Label, req)
		if len(items) == 0 {
			clean = true

			st.report.Tried = append(st.report.Tried, Attempt{
				Backend: b.Name, Result: string(CodeNoResults), Detail: res.Detail,
			})

			continue
		}

		st.report.Tried = append(st.report.Tried, Attempt{Backend: b.Name, Result: resultOK, Detail: res.Detail})
		st.report.ServedBy = b.Name
		st.report.Items = len(items)
		st.items = items

		return st
	}

	// Nothing served: a clean zero when one backend read the window and found nothing, the most actionable
	// failure otherwise (last30days every_path_failed and failure_outcome, reddit_health.py:373-393).
	switch {
	case clean:
		st.report.Code = CodeNoResults
	case len(failed) > 0:
		st.report.Code = slices.MinFunc(failed, func(a, b Code) int { return specificity(a) - specificity(b) })
	default:
		st.report.Code = CodeBroken
		st.report.Tried = append(st.report.Tried, Attempt{
			Backend: "", Result: string(CodeBroken), Detail: "no backend of this source can be asked", Skipped: true,
		})
	}

	return st
}

// inWindow keeps the items dated inside the window, or not dated, at most the request's limit, each stamped with
// where it came from: last30days filter_by_date_range (normalize.py:48-74), which keeps an undated web result only
// on some backends; here an undated item stays, with a low date confidence, since the web search's providers often
// give no date.
func (s *Service) inWindow(items []Item, source, backend, label string, req Request) []Item {
	now := s.now().UTC()

	var out []Item

	for _, it := range items {
		if it.PublishedAt != nil && (it.PublishedAt.Before(req.From) || it.PublishedAt.After(req.To.Add(time.Hour))) {
			continue
		}

		it.Source = source
		it.Backend = backend
		it.Subqueries = []string{label}
		it.FetchedAt = now
		it.ID = itemID(source, it.NativeID)

		it.DateConfidence = "low"
		if it.PublishedAt != nil {
			it.DateConfidence = "high"
		}

		it.Text = clip(it.Text, maxTextRunes)
		it.Title = clip(it.Title, maxTitleRunes)
		out = append(out, it)

		if len(out) == req.Limit {
			break
		}
	}

	return out
}
