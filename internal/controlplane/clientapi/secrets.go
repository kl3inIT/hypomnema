package clientapi

import (
	"context"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The vault, for the owner (issue #108). Only the owner's token may list,
// read, keep or remove a secret; a coordinator's token is refused by the
// policy and has no other operation that returns a value
// (docs/decisions/2026-11-10-owner-reads-secret-values.md). A value is read
// one secret at a time, and each read is recorded.

// Secrets is how the API reaches the vault, which it may not import
// (issue #66): the control plane's own wiring implements it.
type Secrets interface {
	List(ctx context.Context) ([]SecretInfo, error)
	// Value opens one secret: its values by key.
	Value(ctx context.Context, name string) (map[string]string, error)
	// Put keeps values under a name, as a new generic secret or a new
	// version of a secret of any kind.
	Put(ctx context.Context, name string, values map[string]string) (SecretInfo, error)
	Delete(ctx context.Context, name string) error
}

// SecretInfo describes a secret without its value.
type SecretInfo struct {
	Name     string
	Kind     string
	Version  int
	Metadata map[string]string
	// KeyMissing says the root key the secret is sealed under is not in the keyring.
	KeyMissing bool
	// LoginRequired says a token set's refresh token was refused for good.
	LoginRequired bool
	// UsedBy names what refers to the secret: "provider x", "connection github.com:work".
	UsedBy    []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// WithSecrets sets how the owner reaches the vault.
func (s *Server) WithSecrets(v Secrets) *Server {
	s.secrets = v

	return s
}

// errNoSecrets answers when the control plane wired no vault.
var errNoSecrets = fmt.Errorf("%w: this control plane has no vault", orchestration.ErrUnavailable)

func toSecret(i SecretInfo) openapi.Secret {
	out := openapi.Secret{
		Name: i.Name, Kind: i.Kind, Version: i.Version, UsedBy: i.UsedBy,
		KeyMissing: i.KeyMissing, LoginRequired: i.LoginRequired, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
	if out.UsedBy == nil {
		out.UsedBy = []string{}
	}

	if len(i.Metadata) > 0 {
		out.Metadata = &i.Metadata
	}

	return out
}

// ListSecrets describes every secret, without a value.
func (s *Server) ListSecrets(ctx context.Context, _ openapi.ListSecretsRequestObject,
) (openapi.ListSecretsResponseObject, error) {
	if s.secrets == nil {
		return nil, errNoSecrets
	}

	secrets, err := s.secrets.List(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListSecrets200JSONResponse{Data: make([]openapi.Secret, 0, len(secrets))}
	for _, i := range secrets {
		out.Data = append(out.Data, toSecret(i))
	}

	return out, nil
}

// GetSecretValue returns one secret's value to the owner. The read is
// recorded before the value is returned, so a value never leaves unrecorded;
// the value is not logged and the answer is not to be stored.
func (s *Server) GetSecretValue(ctx context.Context, req openapi.GetSecretValueRequestObject,
) (openapi.GetSecretValueResponseObject, error) {
	if s.secrets == nil {
		return nil, errNoSecrets
	}

	values, err := s.secrets.Value(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	if err := s.svc.RecordSecretRead(ctx, req.Name, principal(ctx).ID); err != nil {
		return nil, err
	}

	noStore := "no-store"

	return openapi.GetSecretValue200JSONResponse{
		Body:    openapi.SecretValue{Name: req.Name, Values: values},
		Headers: openapi.GetSecretValue200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

// PutSecret keeps a value under a name. The value is not logged and is not
// in the answer.
func (s *Server) PutSecret(ctx context.Context, req openapi.PutSecretRequestObject,
) (openapi.PutSecretResponseObject, error) {
	if s.secrets == nil {
		return nil, errNoSecrets
	}

	values := map[string]string{}
	if req.Body.Values != nil {
		values = *req.Body.Values
	} else if v := val(req.Body.Value); v != "" {
		values["value"] = v
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("%w: give a value", orchestration.ErrInvalid)
	}

	i, err := s.secrets.Put(ctx, req.Name, values)
	if err != nil {
		return nil, err
	}

	return openapi.PutSecret200JSONResponse(toSecret(i)), nil
}

// DeleteSecret removes a secret; refused while something uses it.
func (s *Server) DeleteSecret(ctx context.Context, req openapi.DeleteSecretRequestObject,
) (openapi.DeleteSecretResponseObject, error) {
	if s.secrets == nil {
		return nil, errNoSecrets
	}

	if err := s.secrets.Delete(ctx, req.Name); err != nil {
		return nil, err
	}

	return openapi.DeleteSecret204Response{}, nil
}
