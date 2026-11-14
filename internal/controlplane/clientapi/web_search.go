package clientapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The Web search page of Settings → Gateway (issue #354, PR 3): the search
// chain in order, each provider with where its search comes from, the
// owner's switch and its state, as OmniRoute lists its search providers
// (src/app/(dashboard)/dashboard/media-providers/[kind]) and 9router its
// web providers (/v1/models/web). An account is added once, on the Models
// page: a subscription is ready here while an account of it is in use.
// Exa's key is entered here and kept in the vault, as OmniRoute and 9router
// take a search provider's key on its own page (a password field); its
// value is never answered. SearXNG's address is entered here too, as
// OmniRoute's and OpenClaw's base URL field.

// WebSearchSettings is the chain and what the owner sets of it.
type WebSearchSettings interface {
	Chain(ctx context.Context) ([]websearch.Link, error)
	SetSearchProvider(ctx context.Context, provider string, on bool) error
	// SetSearXNG keeps SearXNG's address; "" forgets it.
	SetSearXNG(ctx context.Context, address string) error
}

// WithWebSearch gives the API the web search chain and the name of the
// vault secret that holds Exa's key.
func (s *Server) WithWebSearch(w WebSearchSettings, exaSecret string) *Server {
	s.webSearch = w
	s.exaSecret = exaSecret

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

// PutWebSearchExaKey keeps Exa's key in the vault. The key is not logged and
// is not in the answer.
func (s *Server) PutWebSearchExaKey(ctx context.Context, req openapi.PutWebSearchExaKeyRequestObject,
) (openapi.PutWebSearchExaKeyResponseObject, error) {
	if err := s.changeExaKey(ctx, strings.TrimSpace(val(req.Body.Key))); err != nil {
		return nil, err
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.PutWebSearchExaKey200JSONResponse(out), nil
}

// DeleteWebSearchExaKey removes Exa's key; a key that is not there is removed already.
func (s *Server) DeleteWebSearchExaKey(ctx context.Context, _ openapi.DeleteWebSearchExaKeyRequestObject,
) (openapi.DeleteWebSearchExaKeyResponseObject, error) {
	if err := s.changeExaKey(ctx, ""); err != nil {
		return nil, err
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.DeleteWebSearchExaKey200JSONResponse(out), nil
}

// changeExaKey keeps Exa's key, or removes it when key is "".
func (s *Server) changeExaKey(ctx context.Context, key string) error {
	switch {
	case s.webSearch == nil:
		return errNoWebSearch
	case s.secrets == nil:
		return errNoSecrets
	case key != "":
		_, err := s.secrets.Put(ctx, s.exaSecret, map[string]string{"value": key})

		return err
	}

	if err := s.secrets.Delete(ctx, s.exaSecret); err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return err
	}

	return nil
}

// PutWebSearchSearXNG keeps SearXNG's address.
func (s *Server) PutWebSearchSearXNG(ctx context.Context, req openapi.PutWebSearchSearXNGRequestObject,
) (openapi.PutWebSearchSearXNGResponseObject, error) {
	if s.webSearch == nil {
		return nil, errNoWebSearch
	}

	if err := s.webSearch.SetSearXNG(ctx, req.Body.Url); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.PutWebSearchSearXNG200JSONResponse(out), nil
}

// DeleteWebSearchSearXNG forgets SearXNG's address.
func (s *Server) DeleteWebSearchSearXNG(ctx context.Context, _ openapi.DeleteWebSearchSearXNGRequestObject,
) (openapi.DeleteWebSearchSearXNGResponseObject, error) {
	if s.webSearch == nil {
		return nil, errNoWebSearch
	}

	if err := s.webSearch.SetSearXNG(ctx, ""); err != nil {
		return nil, gatewayKeyError(err)
	}

	out, err := s.webSearchChain(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.DeleteWebSearchSearXNG200JSONResponse(out), nil
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

	out := openapi.WebSearch{Chain: make([]openapi.WebSearchLink, 0, len(links)), ExaSecret: s.exaSecret}

	for _, l := range links {
		if l.Address != "" {
			out.SearxngUrl = ptr(l.Address)
		}

		usable := l.Usable && (l.Source != websearch.SourceSubscription || signedIn[l.Provider])
		link := openapi.WebSearchLink{
			Provider: openapi.WebSearchProvider(l.Provider), Source: openapi.WebSearchLinkSource(l.Source), On: l.On,
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

		if !l.RestingUntil.IsZero() {
			link.RestingUntil = ptr(l.RestingUntil)
		}

		out.Chain = append(out.Chain, link)
	}

	return out, nil
}
