package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// An account Google wants verified (issue #822): Antigravity answers a 403 with
// reason VALIDATION_REQUIRED and a link where the owner verifies it. The
// account is marked, kept out of use until its next try, and cleared by a
// call that works or by a new sign-in. Learned from
// opencode-antigravity-auth, which keeps the same four facts on the account
// (src/plugin/storage.ts:202-205) and, in its PR #338, a count of the answers
// that asked, for a growing wait (src/plugin/accounts.ts:53-58, 723-757).

// maxVerifyMessageBytes bounds Google's message as stored.
const maxVerifyMessageBytes = 300

// AccountVerification is what Google said when it asked for an account to be
// verified.
type AccountVerification struct {
	// URL is the latest link Google gave, where the owner verifies the account.
	URL string
	// Message is Google's own words.
	Message string
	// AskedAt is when Google first asked, since the account last worked.
	AskedAt time.Time
	// Failures counts the initial refusal and the retry slots claimed since then.
	Failures int
	// RetryAt is when the account is tried again.
	RetryAt time.Time
}

// VerificationState reads the current mark and the first credential version of the active sign-in.
func (s *Service) VerificationState(ctx context.Context, id string) (*AccountVerification, int, error) {
	row, err := s.read().GetAccount(ctx, id)
	if err != nil {
		return nil, 0, notFound(err, "account", id)
	}

	return verificationFromRow(row), int(row.VerifyLoginVersion), nil
}

// SetVerificationLogin activates freshly stored sign-in credentials and clears the previous mark together.
func (s *Service) SetVerificationLogin(ctx context.Context, id string, version int) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		_, err := q.SetVerificationLogin(ctx, store.SetVerificationLoginParams{ID: id, Now: now, Version: int64(version)})

		return err
	})
}

// ClaimVerificationRetry advances a due mark only if this caller still sees the same retry slot.
func (s *Service) ClaimVerificationRetry(
	ctx context.Context, id string, version int, previous time.Time, wait time.Duration,
) (time.Time, error) {
	var next time.Time
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		at := millis(now).Add(wait)
		rows, err := q.ClaimAccountVerificationRetry(ctx, store.ClaimAccountVerificationRetryParams{
			ID: id, Now: now, Version: int64(version), PreviousRetryAt: validInt(previous.UnixMilli()),
			NextRetryAt: validInt(at.UnixMilli()),
		})
		if rows == 1 && err == nil {
			next = at
		}

		return err
	})

	return next, err
}

func verificationFromRow(row store.Account) *AccountVerification {
	if !row.VerifyAskedAt.Valid {
		return nil
	}

	return &AccountVerification{
		URL: row.VerifyUrl.String, Message: row.VerifyMessage.String, AskedAt: millis(row.VerifyAskedAt.Int64),
		Failures: int(row.VerifyFailures), RetryAt: millis(row.VerifyRetryAt.Int64),
	}
}

// MarkAccountVerification records that Google asked for an account to be
// verified, and returns the mark. A response from a prior sign-in is ignored.
// Once marked, claiming a retry is what advances the schedule; further refusals update the link and message.
func (s *Service) MarkAccountVerification(
	ctx context.Context, id string, version int, url, message string, wait time.Duration,
) (AccountVerification, error) {
	if len(message) > maxVerifyMessageBytes {
		message = message[:maxVerifyMessageBytes]
	}

	var out AccountVerification

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetAccount(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: account %s", ErrNotFound, id)
		}

		if err != nil {
			return fmt.Errorf("reading account %s: %w", id, err)
		}
		if int64(version) < row.VerifyLoginVersion {
			return nil
		}

		out = AccountVerification{
			URL: url, Message: message, AskedAt: millis(now), Failures: 1, RetryAt: millis(now).Add(wait),
		}

		if row.VerifyAskedAt.Valid {
			out.AskedAt = millis(row.VerifyAskedAt.Int64)
			out.Failures, out.RetryAt = int(row.VerifyFailures), millis(row.VerifyRetryAt.Int64)

			if out.URL == "" {
				out.URL = row.VerifyUrl.String
			}
		}

		n, err := q.SetAccountVerification(ctx, store.SetAccountVerificationParams{
			Now: now, Version: int64(version), VerifyUrl: nullString(out.URL), VerifyMessage: nullString(out.Message),
			VerifyAskedAt:  validInt(out.AskedAt.UnixMilli()),
			VerifyFailures: int64(out.Failures), VerifyRetryAt: validInt(out.RetryAt.UnixMilli()), ID: id,
		})

		return oneRow(n, err, "account", id)
	})
	if err != nil {
		return AccountVerification{}, err
	}

	return out, nil
}

// ClearAccountVerification forgets that Google asked for an account to be
// verified; it reports whether it had.
func (s *Service) ClearAccountVerification(ctx context.Context, id string, version int) (bool, error) {
	var cleared bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.ClearAccountVerification(ctx, store.ClearAccountVerificationParams{
			Now: now, ID: id, Version: int64(version),
		})
		if err != nil {
			return fmt.Errorf("clearing the verification of account %s: %w", id, err)
		}

		cleared = n > 0

		return nil
	})

	return cleared, err
}
