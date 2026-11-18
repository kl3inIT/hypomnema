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
// call that works or by a new sign-in (SaveAccountLogin). Learned from
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
	// Failures counts the answers in a row that asked.
	Failures int
	// RetryAt is when the account is tried again.
	RetryAt time.Time
}

// MarkAccountVerification records that Google asked for an account to be
// verified, and returns the mark. wait gives how long the account rests after
// the nth answer in a row that asked. An answer to a request sent before the
// mark was made, while the account rests, changes the link and the message
// but not the count nor the next try, so requests in flight together do not
// climb the schedule at once (PR #338's "set if later", accounts.ts:750-753).
func (s *Service) MarkAccountVerification(
	ctx context.Context, id, url, message string, wait func(failures int) time.Duration,
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

		out = AccountVerification{
			URL: url, Message: message, AskedAt: millis(now), Failures: 1,
		}

		if row.VerifyAskedAt.Valid {
			out.AskedAt = millis(row.VerifyAskedAt.Int64)
			out.Failures = int(row.VerifyFailures) + 1

			if out.URL == "" {
				out.URL = row.VerifyUrl.String
			}
		}

		if row.VerifyAskedAt.Valid && row.VerifyRetryAt.Int64 > now {
			out.Failures, out.RetryAt = int(row.VerifyFailures), millis(row.VerifyRetryAt.Int64)
		} else {
			out.RetryAt = millis(now).Add(wait(out.Failures))
		}

		n, err := q.SetAccountVerification(ctx, store.SetAccountVerificationParams{
			Now: now, VerifyUrl: nullString(out.URL), VerifyMessage: nullString(out.Message),
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
func (s *Service) ClearAccountVerification(ctx context.Context, id string) (bool, error) {
	var cleared bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.ClearAccountVerification(ctx, store.ClearAccountVerificationParams{Now: now, ID: id})
		if err != nil {
			return fmt.Errorf("clearing the verification of account %s: %w", id, err)
		}

		cleared = n > 0

		return nil
	})

	return cleared, err
}
