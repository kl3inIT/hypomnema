package subscription

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// An Antigravity account Google wants verified (issue #822). A 403 with reason
// VALIDATION_REQUIRED, on chat (the gateway), on the models list or on the
// quota read, marks the account with Google's link and message; the account
// rests and is tried again on a growing schedule, and the first call that works
// clears the mark, as a new sign-in does.
//
// Learned from opencode-antigravity-auth: the mark and its four facts
// (src/plugin/storage.ts:202-205 at 821f824), cleared by a new sign-in
// (src/v2.ts:455-462, its issue #589) and on a call that works (PR #338,
// src/plugin.ts:1857 at 853fa29); the schedule 10 minutes, 1 hour, 6 hours,
// then a day (PR #338, src/plugin/accounts.ts:53-58). And from CLIProxyAPI: a
// 403 moves the request to the next credential and rests this one
// (sdk/cliproxy/auth/conductor_cooldown.go:1804-1810, 3969 at a5e8f69).

// verifySchedule is how long an account Google wants verified rests after the
// first, second, third and every later answer in a row that asked (PR #338's
// VERIFICATION_COOLDOWN_SCHEDULE).
var verifySchedule = []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

// verifyWait is the rest after the nth answer in a row that asked.
func verifyWait(failures int) time.Duration {
	return verifySchedule[min(max(failures, 1), len(verifySchedule))-1]
}

// NeedsVerification marks an account Google wants verified, with its link and
// message, and returns when it is tried again.
func (s *Service) NeedsVerification(ctx context.Context, accountID, url, message string) (time.Time, error) {
	v, err := s.accounts.MarkAccountVerification(ctx, accountID, url, message, verifyWait)
	if err != nil {
		return time.Time{}, err
	}

	s.logger.WarnContext(ctx, "Google asks for an account to be verified",
		slog.String("event", "subscription.verification_required"), slog.String("account_id", accountID),
		slog.Int("failures", v.Failures), slog.Time("retry_at", v.RetryAt))

	return v.RetryAt, nil
}

// Verified clears the mark of an account a call worked under.
func (s *Service) Verified(ctx context.Context, accountID string) error {
	cleared, err := s.accounts.ClearAccountVerification(ctx, accountID)
	if err == nil && cleared {
		s.logger.InfoContext(ctx, "an account Google wanted verified works again",
			slog.String("event", "subscription.verification_cleared"), slog.String("account_id", accountID))
	}

	return err
}

// verifyWaits refuses to call Google under an account that rests until its
// next try, so nothing asks it before then; the error carries Google's link.
func (s *Service) verifyWaits(a orchestration.Account) error {
	v := a.Verification
	if v == nil || !v.RetryAt.After(s.clock.Now()) {
		return nil
	}

	return &VerificationError{Message: v.Message, URL: v.URL, AskedAt: v.AskedAt, RetryAt: v.RetryAt}
}

// noteVerification records what a call under an account said of its
// verification: a 403 that asks marks it, one that works clears it. It
// returns the call's error, with the next try when Google asked.
func (s *Service) noteVerification(ctx context.Context, token Token, err error) error {
	var verify *VerificationError

	switch {
	case errors.As(err, &verify):
		retry, markErr := s.NeedsVerification(ctx, token.AccountID, verify.URL, verify.Message)
		if markErr != nil {
			return errors.Join(err, markErr)
		}

		verify.RetryAt = retry

		return err
	case err == nil && token.Verifying:
		if clearErr := s.Verified(ctx, token.AccountID); clearErr != nil {
			s.logger.ErrorContext(ctx, "clearing an account's verification",
				slog.String("event", "subscription.verification_clear_failed"),
				slog.String("account_id", token.AccountID), slog.String("error", clearErr.Error()))
		}

		return nil
	default:
		return err
	}
}
