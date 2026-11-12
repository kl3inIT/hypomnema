package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The Gateway keys page (issue #199, step 6): the keys a client outside, such
// as opencode or omp, gives the gateway. A key is shown once and kept as its
// hash, with its start to tell keys apart, as Coder's AI Gateway keys are
// (GatewayKeysPageView.tsx, CreateGatewayKeyDialog.tsx); it may be limited to
// models, as LiteLLM, Bifrost and 9router limit theirs. The keys made for one
// Dispatch or one chat's pi are only counted.

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
		case k.DispatchID != "" || k.ChatID != "":
			out.LiveWorkerKeys++
		default:
			out.Data = append(out.Data, toGatewayKey(k))
		}
	}

	return out, nil
}

// CreateGatewayKey makes a key and answers its value this once.
func (s *Server) CreateGatewayKey(ctx context.Context, req openapi.CreateGatewayKeyRequestObject,
) (openapi.CreateGatewayKeyResponseObject, error) {
	if s.gatewayKeys == nil {
		return nil, fmt.Errorf("%w: this control plane runs no model gateway", orchestration.ErrUnavailable)
	}

	k, secret, err := s.gatewayKeys.AddKey(ctx, req.Body.Name, val(req.Body.Models))
	if err != nil {
		return nil, gatewayKeyError(err)
	}

	return openapi.CreateGatewayKey201JSONResponse{Key: toGatewayKey(k), Secret: secret}, nil
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
		if k.ID != req.KeyId || k.DispatchID != "" || k.ChatID != "" || k.RevokedAt != nil {
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
	return openapi.GatewayKey{
		Id: k.ID, Name: k.Name, Prefix: k.Prefix, Models: k.Models, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt,
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
	default:
		return err
	}
}
