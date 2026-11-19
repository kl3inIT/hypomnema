package orchestration

import (
	"context"
	"fmt"
)

// GatewayKeysUsingSecret names the gateway keys whose stored versions belong to the secret.
func (s *Service) GatewayKeysUsingSecret(ctx context.Context, name string) ([]string, error) {
	names, err := s.read().ListGatewayKeyNamesBySecret(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("listing the gateway keys that use %q: %w", name, err)
	}

	for index, key := range names {
		names[index] = "gateway key " + key
	}

	return names, nil
}
