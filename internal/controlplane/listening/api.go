// Package listening finds what people say about a topic, for every agent (issue #1287). This file is the API the
// tests are written against, before it is built.
package listening

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

var ErrInvalid = errors.New("invalid plan")

const (
	MaxSubqueries     = 5
	DefaultWindowDays = 30
	MaxWindowDays     = 365
	DefaultResults    = 10
	MaxResults        = 25
	PoolSize          = 40
)

type Code string

const (
	CodeNeedLogin    Code = "need_login"
	CodeRefused      Code = "refused"
	CodeRateLimited  Code = "rate_limited"
	CodeLimitReached Code = "limit_reached"
	CodeNotFound     Code = "not_found"
	CodeTimeout      Code = "timeout"
	CodeBroken       Code = "broken"
	CodeNoResults    Code = "no_results"
)

func Codes() []Code { return nil }

type Failure struct {
	Code   Code
	Detail string
	Until  time.Time
}

func (f *Failure) Error() string { return string(f.Code) }

func Fail(code Code, _ string, _ ...any) *Failure { return &Failure{Code: code} }

func AsFailure(error) *Failure { return &Failure{} }

func HTTPFailure(int, http.Header, time.Time) *Failure { return &Failure{} }

type Plan struct {
	Topic         string             `json:"topic,omitempty"`
	Subqueries    []Subquery         `json:"subqueries"`
	Sources       []string           `json:"sources,omitempty"`
	SourceWeights map[string]float64 `json:"source_weights,omitempty"`
	WindowDays    int                `json:"window_days,omitempty"`
	MaxResults    int                `json:"max_results,omitempty"`
	Backends      map[string]string  `json:"backends,omitempty"`
}

type Subquery struct {
	Label   string   `json:"label,omitempty"`
	Query   string   `json:"query"`
	Sources []string `json:"sources,omitempty"`
	Weight  float64  `json:"weight,omitempty"`
}

type Item struct {
	ID              string             `json:"id"`
	Source          string             `json:"source"`
	SeenIn          []string           `json:"seen_in,omitempty"`
	Backend         string             `json:"backend"`
	NativeID        string             `json:"native_id"`
	URL             string             `json:"url"`
	Title           string             `json:"title,omitempty"`
	Author          string             `json:"author,omitempty"`
	Container       string             `json:"container,omitempty"`
	PublishedAt     *time.Time         `json:"published_at,omitempty"`
	DateConfidence  string             `json:"date_confidence"`
	Text            string             `json:"text,omitempty"`
	Engagement      map[string]float64 `json:"engagement,omitempty"`
	EngagementScore *int               `json:"engagement_score,omitempty"`
	Parent          string             `json:"parent,omitempty"`
	Subqueries      []string           `json:"subqueries"`
	FetchedAt       time.Time          `json:"fetched_at"`
	Score           float64            `json:"score"`
	Cluster         string             `json:"cluster,omitempty"`
}

type Answer struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	Coverage []Coverage `json:"coverage"`
	Clusters []Cluster  `json:"clusters"`
	Items    []Item     `json:"items"`
	Omitted  int        `json:"omitted,omitempty"`
}

const (
	StateOK        = "ok"
	StatePartial   = "partial"
	StateNoResults = "no_results"
	StateNotRead   = "not_read"
)

type Coverage struct {
	Source  string   `json:"source"`
	State   string   `json:"state"`
	Items   int      `json:"items"`
	Code    Code     `json:"code,omitempty"`
	Streams []Stream `json:"streams"`
}

type Stream struct {
	Subquery string    `json:"subquery"`
	ServedBy string    `json:"served_by,omitempty"`
	Items    int       `json:"items"`
	Code     Code      `json:"code,omitempty"`
	Tried    []Attempt `json:"tried"`
}

type Attempt struct {
	Backend string `json:"backend"`
	Result  string `json:"result"`
	Detail  string `json:"detail,omitempty"`
	Until   string `json:"until,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

const (
	SingleSource = "single-source"
	ThinEvidence = "thin-evidence"
)

type Cluster struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Items           []string `json:"items"`
	Representatives []string `json:"representatives"`
	Sources         []string `json:"sources"`
	Score           float64  `json:"score"`
	Uncertainty     string   `json:"uncertainty,omitempty"`
}

type WebSearch func(ctx context.Context, query string, limit int) (websearch.Answer, error)

type Backend struct {
	Name   string
	Host   string
	OptIn  bool
	Probe  func(r Request) *Failure
	Search func(ctx context.Context, r Request) (Result, error)
}

type Result struct {
	Items  []Item
	Detail string
}

type Platform struct {
	Name     string
	Backends []Backend
}

type Request struct {
	Query    string
	Limit    int
	From, To time.Time
	Web      WebSearch
	Pace     func(ctx context.Context, host string, gap time.Duration) (func(), error)
}

type Config struct {
	Platforms []Platform
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration) error
	Logger    *slog.Logger
}

type Service struct{ platforms []Platform }

func New(cfg Config) *Service { return &Service{platforms: cfg.Platforms} }

func (s *Service) SourceNames() []string {
	var out []string
	for _, p := range s.platforms {
		out = append(out, p.Name)
	}

	return out
}

func (s *Service) Search(context.Context, Plan, WebSearch) (Answer, error) { return Answer{}, nil }

type Pacer struct{}

func NewPacer(func() time.Time, func(ctx context.Context, d time.Duration) error) *Pacer { return &Pacer{} }

func (p *Pacer) Wait(context.Context, string, time.Duration) (func(), error) { return func() {}, nil }

func Similarity(_, _, _ string) float64 { return 0 }

func NormalizeURL(raw string) string { return raw }

func Dedupe(items []Item) []Item { return items }

func EngagementScore(string, map[string]float64) (int, bool) { return 0, false }

const (
	SourceWeb    = "web"
	SourceReddit = "reddit"
)

type Endpoints struct {
	RedditURL string
	ArcticURL string
}

func Sources(Endpoints, *http.Client) []Platform {
	return []Platform{{Name: SourceWeb}, {Name: SourceReddit}}
}
