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

var errTokenTransport = errors.New("token endpoint transport failed")

func (a Account) claudeSetupToken() bool {
	return a.Runtime == claudeRuntime && a.secretKind == vault.KindGeneric
}

func unusableToken(ctx context.Context, err error) bool {
	var transport net.Error
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errTokenTransport) || errors.As(err, &transport) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, vault.ErrConflict) {
		return false
	}
	return errors.Is(err, ErrNoTokens) || errors.Is(err, vault.ErrNotFound) ||
		errors.Is(err, vault.ErrRefreshFailed) || errors.Is(err, vault.ErrInvalid) || errors.Is(err, vault.ErrTampered)
}

func (s *Service) failedRefusedToken(ctx context.Context, a Account, version int, err error) error {
	if !unusableToken(ctx, err) {
		return err
	}
	s.markLoginRequired(ctx, a, version)
	return fmt.Errorf("%w: %w", vault.ErrLoginRequired, err)
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
