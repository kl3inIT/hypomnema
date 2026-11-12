// Package skillssh asks skills.sh, the directory of Agent Skills, for the
// skills that answer a search and for what its auditors said of one (issue
// #21, step 5). It does what the skills CLI does (MIT, Copyright (c) 2026
// Vercel, Inc., https://github.com/vercel-labs/skills): src/find.ts for the
// search, src/telemetry.ts (fetchAuditData) for the audits. Nothing is sent
// to skills.sh besides the questions: no telemetry.
package skillssh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// ErrUnavailable is skills.sh not answering.
var ErrUnavailable = errors.New("skills.sh unavailable")

// The skills CLI's numbers (src/find.ts): fewer exact results than this and
// the query is asked again without each of its words, up to this many words,
// and no more results than this are given.
const (
	minExactResults  = 10
	maxFallbackTerms = 6
	maxMergedResults = 25
)

// maxBody bounds what is read of an answer.
const maxBody = 4 << 20

// Result is one skill skills.sh found.
type Result struct {
	// ID is owner/repo/slug, the skill's page on skills.sh.
	ID   string
	Name string
	// Source is owner/repo.
	Source   string
	Installs int64
}

// Audit is what one of skills.sh's auditors said of a skill.
type Audit struct {
	// Partner is the auditor as skills.sh names it: ath (Gen), socket, snyk.
	Partner    string
	Risk       string
	Alerts     *int
	AnalyzedAt string
}

// Client asks skills.sh. Search and Audit are its addresses unless a test sets them.
type Client struct {
	HTTP   *http.Client
	Search string
	Audit  string
}

// New asks skills.sh with client.
func New(client *http.Client) *Client {
	return &Client{
		HTTP:   client,
		Search: "https://skills.sh/api/search",
		Audit:  "https://www.skills.sh/tele/audit",
	}
}

// Find is the skills CLI's searchSkills: the query as given, then, when it
// finds fewer than ten and has two to six words, the query without each
// word in turn. Those results follow the exact ones, the ones more queries
// found first, then the better placed, then the more installed, up to 25 in
// all. exact is how many lead.
func (c *Client) Find(ctx context.Context, query, owner string) (results []Result, exact int, err error) {
	found, err := c.search(ctx, strings.TrimSpace(query), owner)
	if err != nil {
		return nil, 0, err
	}

	terms := unique(strings.Fields(query))
	if len(found) >= minExactResults || len(terms) < 2 || len(terms) > maxFallbackTerms {
		return found, len(found), nil
	}

	type candidate struct {
		result    Result
		hits      int
		bestRank  int
		firstSeen int
	}

	seen := make(map[string]bool, len(found))
	for _, r := range found {
		seen[r.ID] = true
	}

	candidates := map[string]*candidate{}

	for i := range terms {
		rest := append(append([]string{}, terms[:i]...), terms[i+1:]...)

		// A query that fails adds nothing, as in the CLI.
		more, _ := c.search(ctx, strings.Join(rest, " "), owner)

		for rank, r := range more {
			if seen[r.ID] {
				continue
			}

			if e, ok := candidates[r.ID]; ok {
				e.hits++
				e.bestRank = min(e.bestRank, rank)

				continue
			}

			candidates[r.ID] = &candidate{result: r, hits: 1, bestRank: rank, firstSeen: len(candidates)}
		}
	}

	partial := make([]*candidate, 0, len(candidates))
	for _, e := range candidates {
		partial = append(partial, e)
	}

	sort.Slice(partial, func(i, j int) bool {
		a, b := partial[i], partial[j]
		switch {
		case a.hits != b.hits:
			return a.hits > b.hits
		case a.bestRank != b.bestRank:
			return a.bestRank < b.bestRank
		case a.result.Installs != b.result.Installs:
			return a.result.Installs > b.result.Installs
		default:
			return a.firstSeen < b.firstSeen
		}
	})

	results = found
	for _, e := range partial[:min(len(partial), max(0, maxMergedResults-len(found)))] {
		results = append(results, e.result)
	}

	return results, len(found), nil
}

func unique(words []string) []string {
	var out []string

	for _, w := range words {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}

	return out
}

func (c *Client) search(ctx context.Context, query, owner string) ([]Result, error) {
	params := url.Values{"q": {query}}
	if owner != "" {
		params.Set("owner", owner)
	}

	var body struct {
		Skills []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Installs int64  `json:"installs"`
			Source   string `json:"source"`
		} `json:"skills"`
	}

	if err := c.get(ctx, c.Search+"?"+params.Encode(), &body); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(body.Skills))
	for _, s := range body.Skills {
		if s.ID == "" || s.Name == "" {
			continue
		}

		out = append(out, Result{ID: s.ID, Name: s.Name, Source: s.Source, Installs: s.Installs})
	}

	return out, nil
}

// Audits is what skills.sh's auditors said of the skill slug of source
// (owner/repo), by auditor. None when skills.sh has none.
func (c *Client) Audits(ctx context.Context, source, slug string) ([]Audit, error) {
	params := url.Values{"source": {source}, "skills": {slug}}

	var body map[string]map[string]struct {
		Risk       string `json:"risk"`
		Alerts     *int   `json:"alerts"`
		AnalyzedAt string `json:"analyzedAt"`
	}

	if err := c.get(ctx, c.Audit+"?"+params.Encode(), &body); err != nil {
		return nil, err
	}

	out := []Audit{}
	for partner, a := range body[slug] {
		out = append(out, Audit{Partner: partner, Risk: a.Risk, Alerts: a.Alerts, AnalyzedAt: a.AnalyzedAt})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Partner < out[j].Partner })

	return out, nil
}

func (c *Client) get(ctx context.Context, address string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("asking skills.sh: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hypomnema")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: skills.sh answered %s", ErrUnavailable, resp.Status)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(into); err != nil {
		return fmt.Errorf("%w: reading skills.sh's answer: %w", ErrUnavailable, err)
	}

	return nil
}
