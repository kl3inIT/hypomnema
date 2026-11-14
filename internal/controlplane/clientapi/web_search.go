package clientapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The Web search page of Settings → Gateway (issues #354, #393): the search
// chain in order, each provider with where its search comes from, the
// owner's switch, its state, its keys and how much of it is used, as
// OmniRoute lists its search providers
// (src/app/(dashboard)/dashboard/media-providers/[kind]) and search-rotation
// its engines with their balances (static/app.js). An account is added once,
// on the Models page: a subscription is ready here while an account of it is
// in use. A provider's keys are entered here and kept in the vault, one
// secret a key (websearch.KeyName), as OmniRoute and 9router take a search
// provider's key on its own page (a password field) and mcp-nexus keeps a
// pool of Tavily keys; a value is never answered.

// WebSearchSettings is the chain and what the owner sets of it.
type WebSearchSettings interface {
	Chain(ctx context.Context) ([]websearch.Link, error)
	SetSearchProvider(ctx context.Context, provider string, on bool) error
	SetSearchOrder(ctx context.Context, providers []string) error
}

// WithWebSearch gives the API the web search chain.
func (s *Server) WithWebSearch(w WebSearchSettings) *Server {
	s.webSearch = w

	return s
}

var errNoWebSearch = fmt.Errorf("%w: this control plane has no web search", orchestration.ErrUnavailable)

// GetWebSearch answers the chain.
func (s *Server) GetWebSearch(ctx context.Context, _ openapi.GetWebSearchRequestObject,
) (openapi.GetWebSearchResponseObject, error) {
	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetWebSearch200JSONResponse(out), nil
}

// SetWebSearchProvider switches a provider on or off and answers the chain.
func (s *Server) SetWebSearchProvider(ctx context.Context, req openapi.SetWebSearchProviderRequestObject,
) (openapi.SetWebSearchProviderResponseObject, error) {
	if s.webSearch == nil {
		return nil, errNoWebSearch
	}

	if err := s.webSearch.SetSearchProvider(ctx, string(req.Provider), req.Body.On); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetWebSearchProvider200JSONResponse(out), nil
}

// SetWebSearchOrder keeps the keyed providers' order and answers the chain.
func (s *Server) SetWebSearchOrder(ctx context.Context, req openapi.SetWebSearchOrderRequestObject,
) (openapi.SetWebSearchOrderResponseObject, error) {
	if s.webSearch == nil {
		return nil, errNoWebSearch
	}

	order := make([]string, 0, len(req.Body.Providers))
	for _, p := range req.Body.Providers {
		order = append(order, string(p))
	}

	if err := s.webSearch.SetSearchOrder(ctx, order); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.SetWebSearchOrder200JSONResponse(out), nil
}

// AddWebSearchKey keeps one more key of a provider in the vault, under the
// lowest free name. The key is not logged and is not in the answer.
func (s *Server) AddWebSearchKey(ctx context.Context, req openapi.AddWebSearchKeyRequestObject,
) (openapi.AddWebSearchKeyResponseObject, error) {
	key := strings.TrimSpace(val(req.Body.Key))

	switch {
	case s.webSearch == nil:
		return nil, errNoWebSearch
	case s.secrets == nil:
		return nil, errNoSecrets
	case key == "":
		return nil, fmt.Errorf("%w: the key is empty", orchestration.ErrInvalid)
	}

	secrets, err := s.secrets.List(ctx)
	if err != nil {
		return nil, err
	}

	taken := make([]string, 0, len(secrets))
	for _, i := range secrets {
		taken = append(taken, i.Name)
	}

	name := websearch.NextKeyName(string(req.Provider), taken)
	if _, err := s.secrets.Put(ctx, name, map[string]string{"value": key}); err != nil {
		return nil, err
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.AddWebSearchKey201JSONResponse{Name: name, WebSearch: out}, nil
}

// RemoveWebSearchKey removes one of a provider's keys; a key that is not
// there is removed already.
func (s *Server) RemoveWebSearchKey(ctx context.Context, req openapi.RemoveWebSearchKeyRequestObject,
) (openapi.RemoveWebSearchKeyResponseObject, error) {
	switch provider, _, ok := websearch.ParseKeyName(req.Name); {
	case s.webSearch == nil:
		return nil, errNoWebSearch
	case s.secrets == nil:
		return nil, errNoSecrets
	case !ok || provider != string(req.Provider):
		return nil, fmt.Errorf("%w: %q is not a name of %s's keys", orchestration.ErrInvalid, req.Name, req.Provider)
	}

	if err := s.secrets.Delete(ctx, req.Name); err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return nil, err
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.RemoveWebSearchKey200JSONResponse(out), nil
}

func (s *Server) webSearchChain(ctx context.Context) (openapi.WebSearch, error) {
	if s.webSearch == nil {
		return openapi.WebSearch{}, errNoWebSearch
	}

	links, err := s.webSearch.Chain(ctx)
	if err != nil {
		return openapi.WebSearch{}, err
	}

	// A subscription searches under its accounts that are in use and signed in here, as the gateway's chat
	// takes them (subscription.Service.Token).
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		return openapi.WebSearch{}, err
	}

	signedIn := map[string]bool{}

	for _, a := range accounts {
		if a.Status == orchestration.AccountActive && a.State == subscription.StateActive {
			signedIn[a.Runtime] = true
		}
	}

	out := openapi.WebSearch{Chain: make([]openapi.WebSearchLink, 0, len(links))}

	for _, l := range links {
		usable := l.Usable && (l.Source != websearch.SourceSubscription || signedIn[l.Provider])
		link := openapi.WebSearchLink{
			Provider: openapi.WebSearchProvider(l.Provider), Source: openapi.WebSearchLinkSource(l.Source), On: l.On,
			Usage: toWebSearchUsage(l.Usage), RestingUntil: timePtr(l.RestingUntil), Rest: restPtr(l.Rest),
		}

		switch {
		case !l.On:
			link.State = openapi.WebSearchLinkStateOff
		case !usable:
			link.State = openapi.WebSearchLinkStateNotSetUp
		case !l.RestingUntil.IsZero():
			link.State = openapi.WebSearchLinkStateResting
		default:
			link.State = openapi.WebSearchLinkStateReady
		}

		if l.Source == websearch.SourceKey {
			keys := make([]openapi.WebSearchKey, 0, len(l.Keys))
			for _, k := range l.Keys {
				keys = append(keys, toWebSearchKey(k))
			}

			link.Keys = &keys
		}

		out.Chain = append(out.Chain, link)
	}

	return out, nil
}

func toWebSearchKey(k websearch.KeyState) openapi.WebSearchKey {
	out := openapi.WebSearchKey{
		Name: k.Name, State: openapi.WebSearchKeyStateReady, Usage: toWebSearchUsage(k.Usage),
		RestingUntil: timePtr(k.RestingUntil), Rest: restPtr(k.Rest), LastUsedAt: timePtr(k.LastUsed),
	}

	switch {
	case !k.RestingUntil.IsZero():
		out.State = openapi.WebSearchKeyStateResting
	case k.Spent:
		out.State = openapi.WebSearchKeyStateSpent
	}

	if k.Balance != nil {
		out.Balance = &openapi.WebSearchBalance{
			Used: float32(k.Balance.Used), Limit: float32(k.Balance.Limit), Remaining: float32(k.Balance.Remaining),
			CheckedAt: k.Balance.CheckedAt,
		}
	}

	return out
}

func toWebSearchUsage(c websearch.Count) openapi.WebSearchUsage {
	out := openapi.WebSearchUsage{
		Today: openapi.WebSearchTally{Answered: c.Today.Answered, Failed: c.Today.Failed},
		Month: openapi.WebSearchTally{Answered: c.Month.Answered, Failed: c.Month.Failed},
	}

	if c.MonthDollars > 0 {
		out.MonthDollars = ptr(float32(c.MonthDollars))
	}

	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return &t
}

func restPtr(why string) *openapi.WebSearchRest {
	if why == "" {
		return nil
	}

	r := openapi.WebSearchRest(why)

	return &r
}
