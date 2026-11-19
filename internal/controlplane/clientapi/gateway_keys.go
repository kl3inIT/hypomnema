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

	k, secret, err := s.gatewayKeys.AddKey(ctx, req.Body.Name, val(req.Body.Models), gatewayEndpoints(req.Body.Endpoints))
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

	return openapi.GatewayKey{
		Id: k.ID, Name: k.Name, Prefix: k.Prefix, Models: k.Models, Endpoints: endpoints, CreatedAt: k.CreatedAt,
		LastUsedAt: k.LastUsedAt, MaskedValue: ptr(k.MaskedValue),
	}
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
