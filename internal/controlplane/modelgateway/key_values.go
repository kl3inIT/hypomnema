package modelgateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// KeyUpdate changes only the fields supplied. A non-nil empty model list means every model;
// a non-nil empty endpoint list means chat only, and [AllEndpoints] means every kind.
type KeyUpdate struct {
	Name      *string
	Models    []string
	Endpoints []Endpoint
}

// AllEndpoints resets an owner key's endpoint access to every kind, including future kinds.
const AllEndpoints Endpoint = "*"

// UpdateKey edits an active owner-made key without replacing its value or id.
func (s *Service) UpdateKey(ctx context.Context, id string, change KeyUpdate) (Key, error) {
	params := store.UpdateGatewayKeyParams{ID: id}
	if change.Name != nil {
		if !keyNamePattern.MatchString(*change.Name) {
			return Key{}, fmt.Errorf("%w: a key's name is lowercase letters, digits and dashes", ErrInvalid)
		}

		params.Name = sql.NullString{String: *change.Name, Valid: true}
	}

	endpoints := change.Endpoints
	if len(endpoints) == 1 && endpoints[0] == AllEndpoints {
		endpoints = nil
	}

	if err := validateKeyAccess(change.Models, endpoints); err != nil {
		return Key{}, err
	}

	if change.Models != nil {
		models := change.Models
		if len(models) == 0 {
			models = []string{AllModels}
		}

		encoded, err := json.Marshal(models)
		if err != nil {
			return Key{}, fmt.Errorf("encoding the key's models: %w", err)
		}

		params.Models = sql.NullString{String: string(encoded), Valid: true}
	}

	kinds, err := keyEndpointsJSON(endpoints)
	if err != nil {
		return Key{}, err
	}
	params.Endpoints = kinds
	params.SetEndpoints = change.Endpoints != nil

	var updated Key

	err = s.write(ctx, func(q *store.Queries, _ int64) error {
		if _, err := liveOwnerKey(ctx, q, id); err != nil {
			return err
		}

		row, err := q.UpdateGatewayKey(ctx, params)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: key name %q is taken", ErrConflict, params.Name.String)
		}
		if err != nil {
			return fmt.Errorf("updating key %s: %w", id, err)
		}

		updated, err = keyFromRow(row)
		if err != nil {
			return err
		}

		lastUsed, err := q.GatewayKeyLastUsed(ctx, id)
		switch {
		case err == nil:
			usedAt := time.UnixMilli(lastUsed).UTC()
			updated.LastUsedAt = &usedAt
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("reading when key %s was last used: %w", id, err)
		}

		return nil
	})

	return updated, err
}

// KeyValue opens the stored value of an active owner-made key, if it has one.
func (s *Service) KeyValue(ctx context.Context, id string) (string, error) {
	row, err := liveOwnerKey(ctx, store.New(s.db), id)
	if err != nil {
		return "", err
	}
	if !row.SecretVersionID.Valid {
		return "", fmt.Errorf("%w: this gateway key has no stored value", ErrNotFound)
	}
	if s.keyVault == nil {
		return "", keyVaultError(vault.ErrNotConfigured)
	}

	opened, err := s.keyVault.OpenVersion(ctx, row.SecretVersionID.String)
	if err != nil {
		return "", keyVaultError(err)
	}

	value := opened.Payload[vault.KeyValue]
	if bearertoken.Hash(value) != row.KeyHash {
		return "", fmt.Errorf("gateway key %s: %w", id, vault.ErrTampered)
	}

	return value, nil
}

func keyVaultError(err error) error {
	switch {
	case errors.Is(err, vault.ErrNotConfigured), errors.Is(err, vault.ErrKeyUnknown):
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	case errors.Is(err, vault.ErrNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	default:
		return err
	}
}

func liveOwnerKey(ctx context.Context, q *store.Queries, id string) (store.GatewayKey, error) {
	row, err := q.GetLiveOwnerGatewayKey(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("%w: gateway key %s", ErrNotFound, id)
	}
	if err != nil {
		return row, fmt.Errorf("reading gateway key %s: %w", id, err)
	}

	return row, nil
}
