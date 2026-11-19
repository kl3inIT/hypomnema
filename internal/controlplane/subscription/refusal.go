package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// ErrSetupToken says a Claude token added by hand cannot be refreshed.
var ErrSetupToken = errors.New("a Claude setup token cannot be refreshed")

// ErrTokenUnavailable tells the gateway to try another account for this request.
var ErrTokenUnavailable = errors.New("the account token is unavailable")

var errTokenTransport = errors.New("token endpoint transport failed")

func (a Account) claudeSetupToken() bool {
	return a.Runtime == claudeRuntime && a.secretKind == vault.KindGeneric
}

func (s *Service) skipFailedToken(ctx context.Context, a Account, version int, err error) bool {
	var transport net.Error
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errTokenTransport) || errors.As(err, &transport) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}

	switch {
	case errors.Is(err, vault.ErrLoginRequired), errors.Is(err, vault.ErrRefreshFailed), errors.Is(err, vault.ErrConflict):
		return true
	case errors.Is(err, vault.ErrLoginRejected), errors.Is(err, ErrNoTokens), errors.Is(err, vault.ErrNotFound),
		errors.Is(err, vault.ErrInvalid), errors.Is(err, vault.ErrTampered):
		s.markLoginRequired(ctx, a, version)
		return true
	default:
		return false
	}
}

func (s *Service) failedRefusedToken(ctx context.Context, a Account, version int, err error) error {
	if !s.skipFailedToken(ctx, a, version, err) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrTokenUnavailable, err)
}

// MarkTokenRefused records a 401 that remains after retrying a credential.
func (s *Service) MarkTokenRefused(ctx context.Context, refused Token) error {
	a, err := s.Get(ctx, refused.AccountID)
	if err != nil {
		return err
	}
	return s.setLoginRequired(ctx, a, refused.Version)
}

func (s *Service) markLoginRequired(ctx context.Context, a Account, version int) {
	if err := s.setLoginRequired(ctx, a, version); err != nil {
		s.logger.WarnContext(ctx, "marking an unusable account", slog.String("account_id", a.ID), slog.Any("error", err))
	}
}

func (s *Service) setLoginRequired(ctx context.Context, a Account, version int) error {
	name, ok := vault.NameFromRef(a.SecretRef)
	if !ok || version == 0 || a.claudeSetupToken() || a.State == StateLoginRequired {
		return nil
	}
	return s.vault.MarkLoginRequired(ctx, name, version)
}
