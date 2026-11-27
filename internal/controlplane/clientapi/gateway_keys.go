package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// ListGatewayKeys lists the keys the owner made that still work.
func (s *Server) ListGatewayKeys(ctx context.Context, _ openapi.ListGatewayKeysRequestObject,
) (openapi.ListGatewayKeysResponseObject, error) {
	out := openapi.ListGatewayKeys200JSONResponse{Data: []openapi.GatewayKey{}}
	if s.gatewayKeys == nil {
		return out, nil
	}

	keys, err := s.gatewayKeys.ListKeys(ctx)
	if err != nil {
		return nil, err
	}

	for _, k := range keys {
		switch {
		case k.RevokedAt != nil:
		case !k.OwnerMade():
			out.LiveWorkerKeys++
		default:
			out.Data = append(out.Data, toGatewayKey(k))
		}
	}

	return out, nil
}

// CreateGatewayKey makes an owner key and returns its value.
func (s *Server) CreateGatewayKey(ctx context.Context, req openapi.CreateGatewayKeyRequestObject,
) (openapi.CreateGatewayKeyResponseObject, error) {
	if s.gatewayKeys == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	k, secret, err := s.gatewayKeys.AddRoutedKey(ctx, req.Body.Name, val(req.Body.Models),
		gatewayEndpoints(req.Body.Endpoints), modelgateway.KeyRouting{
			Accounts: val(req.Body.Accounts), SearchOrder: searchOrder(req.Body.SearchOrder),
		})
	if err != nil {
		return nil, gatewayKeyError(err)
	}

	return openapi.CreateGatewayKey201JSONResponse{Key: toGatewayKey(k), Secret: secret}, nil
}

// UpdateGatewayKey edits a key's name or access without changing its value.
func (s *Server) UpdateGatewayKey(ctx context.Context, req openapi.UpdateGatewayKeyRequestObject,
) (openapi.UpdateGatewayKeyResponseObject, error) {
	if s.gatewayKeys == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	key, err := s.gatewayKeys.UpdateKey(ctx, req.KeyId, modelgateway.KeyUpdate{
		Name: req.Body.Name, Models: val(req.Body.Models), Endpoints: gatewayEndpoints(req.Body.Endpoints),
		Accounts: val(req.Body.Accounts), SearchOrder: searchOrder(req.Body.SearchOrder),
	})
	if err != nil {
		return nil, gatewayKeyError(err)
	}

	return openapi.UpdateGatewayKey200JSONResponse(toGatewayKey(key)), nil
}

// GetGatewayKeyValue reads the value of an owner-made key from the vault.
func (s *Server) GetGatewayKeyValue(ctx context.Context, req openapi.GetGatewayKeyValueRequestObject,
) (openapi.GetGatewayKeyValueResponseObject, error) {
	if s.gatewayKeys == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	value, err := s.gatewayKeys.KeyValue(ctx, req.KeyId)
	if err != nil {
		return nil, gatewayKeyError(err)
	}

	noStore := "no-store"

	return openapi.GetGatewayKeyValue200JSONResponse{
		Body:    openapi.GatewayKeyValue{Secret: value},
		Headers: openapi.GetGatewayKeyValue200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

// GetGatewayToolConfig writes the file that points a tool at the gateway with an owner-made key (issue #907).
func (s *Server) GetGatewayToolConfig(ctx context.Context, req openapi.GetGatewayToolConfigRequestObject,
) (openapi.GetGatewayToolConfigResponseObject, error) {
	if s.gatewayKeys == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	c, err := s.gatewayKeys.ToolConfig(ctx, req.KeyId, modelgateway.Tool(req.Tool), val(req.Params.Model),
		req.Params.BaseUrl)
	if err != nil {
		return nil, gatewayKeyError(err)
	}

	out := openapi.GatewayToolConfig{
		Tool: openapi.GatewayTool(c.Tool), Models: c.Models, Model: c.Model, KeyStored: c.KeyStored,
		Files: make([]openapi.GatewayToolFile, 0, len(c.Files)),
	}

	for _, f := range c.Files {
		out.Files = append(out.Files, openapi.GatewayToolFile{
			Path: f.Path, Content: f.Content, Format: f.Format,
		})
	}

	noStore := "no-store"

	return openapi.GetGatewayToolConfig200JSONResponse{
		Body: out, Headers: openapi.GetGatewayToolConfig200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

func gatewayEndpoints[Endpoint ~string](endpoints *[]Endpoint) []modelgateway.Endpoint {
	if endpoints == nil {
		return nil
	}

	out := make([]modelgateway.Endpoint, 0, len(*endpoints))
	for _, endpoint := range *endpoints {
		out = append(out, modelgateway.Endpoint(endpoint))
	}

	return out
}

// searchOrder is a key's own web search order as the gateway takes it; nil when none is given.
func searchOrder(order *[]openapi.WebSearchProvider) []string {
	if order == nil {
		return nil
	}

	out := make([]string, 0, len(*order))
	for _, p := range *order {
		out = append(out, string(p))
	}

	return out
}

// RevokeGatewayKey stops a key the owner made. The keys of a Dispatch or a
// chat are not the owner's to revoke here: they end with what they were made for.
func (s *Server) RevokeGatewayKey(ctx context.Context, req openapi.RevokeGatewayKeyRequestObject,
) (openapi.RevokeGatewayKeyResponseObject, error) {
	if s.gatewayKeys == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	keys, err := s.gatewayKeys.ListKeys(ctx)
	if err != nil {
		return nil, err
	}

	for _, k := range keys {
		// A key revoked is not on the page any more: it is not found, as one never made.
		if k.ID != req.KeyId || !k.OwnerMade() || k.RevokedAt != nil {
			continue
		}

		if err := s.gatewayKeys.RevokeKey(ctx, k.ID); err != nil {
			return nil, gatewayKeyError(err)
		}

		return openapi.RevokeGatewayKey204Response{}, nil
	}

	return nil, fmt.Errorf("%w: gateway key %s", orchestration.ErrNotFound, req.KeyId)
}

func toGatewayKey(k modelgateway.Key) openapi.GatewayKey {
	endpoints := []openapi.GatewayEndpoint{}

	for _, e := range modelgateway.Endpoints {
		if k.Reaches(e) {
			endpoints = append(endpoints, openapi.GatewayEndpoint(e))
		}
	}

	order := make([]openapi.WebSearchProvider, 0, len(k.SearchOrder))
	for _, p := range k.SearchOrder {
		order = append(order, openapi.WebSearchProvider(p))
	}

	out := openapi.GatewayKey{
		Id: k.ID, Name: k.Name, Prefix: k.Prefix, Models: k.Models, Endpoints: endpoints, SearchOrder: order,
		CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt, MaskedValue: ptr(k.MaskedValue),
	}

	if k.OwnAccounts() {
		accounts := make([]openapi.GatewayKeyAccount, 0, len(k.Accounts))
		for _, a := range k.Accounts {
			accounts = append(accounts, openapi.GatewayKeyAccount{Id: a.ID, Runtime: a.Runtime})
		}

		out.Accounts = &accounts
	}

	return out
}

// gatewayKeyError says a gateway's refusal as the API's.
func gatewayKeyError(err error) error {
	switch {
	case errors.Is(err, modelgateway.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	case errors.Is(err, modelgateway.ErrNotFound):
		return fmt.Errorf("%w: %w", orchestration.ErrNotFound, err)
	case errors.Is(err, modelgateway.ErrConflict):
		return fmt.Errorf("%w: %w", orchestration.ErrConflict, err)
	case errors.Is(err, modelgateway.ErrUnavailable):
		return fmt.Errorf("%w: %w", orchestration.ErrUnavailable, err)
	default:
		return err
	}
}
