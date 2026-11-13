package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Accounts the owner logged in through the control plane (issue #20, step 2).
// Their tokens are a secret of the vault, named by SecretRef; this package
// keeps who the account is at the vendor and how its refreshes went, and
// never a token. Placement leaves them alone: a worker runs under an account
// whose CLI was logged in on its node, until tokens are handed to nodes.

// maxRefreshErrorBytes bounds the reason of a failed refresh as stored.
const maxRefreshErrorBytes = 300

// AccountLogin is who a login turned out to be.
type AccountLogin struct {
	Runtime string
	// ExternalID is the vendor's own id of the account; a new login with the
	// same one and the same Email keeps the account and its secret. Both are
	// asked because Codex's id is the workspace's, shared by its members
	// (issue #326; omp keys a Codex credential by e-mail and workspace).
	ExternalID string
	Email      string
	Plan       string
	// Project is the vendor's project the account works in, when it has one.
	Project string
	// Label names the account to the owner; empty means the email.
	Label string
}

// AccountRefresh is how an account's refreshes are going.
type AccountRefresh struct {
	// Failures counts the refreshes that failed in a row for a reason that may pass.
	Failures int
	// RetryAt is when the next one may be tried.
	RetryAt *time.Time
	// Error says why the last one failed. It holds no token.
	Error string
}

// SaveAccountLogin records a login: a new account, or the one this runtime
// already has for the vendor's id and the e-mail, with its email and plan as they are now.
// secretRef gives the reference to the secret a new account's tokens go to.
func (s *Service) SaveAccountLogin(
	ctx context.Context, in AccountLogin, secretRef func(accountID string) string,
) (Account, error) {
	in.ExternalID, in.Label = strings.TrimSpace(in.ExternalID), strings.TrimSpace(in.Label)
	if in.Label == "" {
		in.Label = in.Email
	}

	if in.Runtime == "" || in.ExternalID == "" || in.Label == "" {
		return Account{}, fmt.Errorf("%w: a login needs a runtime, the vendor's account id and a label", ErrInvalid)
	}

	var id string

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetAccountByExternalID(ctx, store.GetAccountByExternalIDParams{
			Runtime: in.Runtime, ExternalID: nullString(in.ExternalID), Email: strings.TrimSpace(in.Email),
		})
		if err == nil {
			id = row.ID

			n, err := q.SetAccountLogin(ctx, store.SetAccountLoginParams{
				Now: now, Email: nullString(in.Email), Plan: nullString(in.Plan), Project: nullString(in.Project), ID: id,
			})

			return oneRow(n, err, "account", id)
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reading the account of %s at %s: %w", in.ExternalID, in.Runtime, err)
		}

		id = s.newID("acc")

		if err := q.InsertLoginAccount(ctx, store.InsertLoginAccountParams{
			ID: id, Runtime: in.Runtime, Label: in.Label, ExternalID: nullString(in.ExternalID),
			Email: nullString(in.Email), Plan: nullString(in.Plan), Project: nullString(in.Project),
			SecretRef: nullString(secretRef(id)), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting account %s/%s: %w", ErrConflict, in.Runtime, in.Label, err)
		}

		return nil
	})
	if err != nil {
		return Account{}, fmt.Errorf("recording a login: %w", err)
	}

	return s.getAccount(ctx, id)
}

// SetAccountPlan records the plan a refresh reported.
func (s *Service) SetAccountPlan(ctx context.Context, id, email, plan string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetAccountIdentity(ctx, store.SetAccountIdentityParams{
			Now: now, Email: nullString(email), Plan: nullString(plan), ID: id,
		})

		return oneRow(n, err, "account", id)
	})
}

// RecordAccountRefresh records how an account's refreshes are going; the zero
// value says the last one worked.
func (s *Service) RecordAccountRefresh(ctx context.Context, id string, r AccountRefresh) error {
	if len(r.Error) > maxRefreshErrorBytes {
		r.Error = r.Error[:maxRefreshErrorBytes]
	}

	var retryAt sql.NullInt64
	if r.RetryAt != nil {
		retryAt = validInt(r.RetryAt.UnixMilli())
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetAccountRefresh(ctx, store.SetAccountRefreshParams{
			Now: now, RefreshFailures: int64(r.Failures), RefreshRetryAt: retryAt,
			RefreshError: nullString(r.Error), ID: id,
		})

		return oneRow(n, err, "account", id)
	})
}

// RemoveAccountLogin removes an account the owner logged in through the
// control plane. Its secret is the caller's to remove afterwards.
func (s *Service) RemoveAccountLogin(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteLoginAccount(ctx, id)
		if err != nil {
			return fmt.Errorf("deleting account %s: %w", id, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: no account %s that was logged in here", ErrNotFound, id)
		}

		return nil
	})
}

// AddTokenAccount records an account whose token the control plane keeps and
// sends to the node with each Dispatch. secretRef names the vault's secret for
// the new account's id; the caller stores the token there.
func (s *Service) AddTokenAccount(
	ctx context.Context, runtime, label string, secretRef func(accountID string) string,
) (Account, error) {
	label = strings.TrimSpace(label)

	switch {
	case runtime != TokenRuntime:
		return Account{}, fmt.Errorf("%w: only a %s account is a token sent to a node", ErrInvalid, TokenRuntime)
	case label == "":
		return Account{}, fmt.Errorf("%w: an account needs a label", ErrInvalid)
	}

	id := s.newID("acc")

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertTokenAccount(ctx, store.InsertTokenAccountParams{
			ID: id, Runtime: runtime, Label: label, SecretRef: nullString(secretRef(id)), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting account %s/%s: %w", ErrConflict, runtime, label, err)
		}

		return nil
	})
	if err != nil {
		return Account{}, fmt.Errorf("adding an account: %w", err)
	}

	return s.getAccount(ctx, id)
}

// AccountTokenRef returns the secret ref of an account whose token is sent to
// the node, and false for any other account.
func (s *Service) AccountTokenRef(ctx context.Context, accountID string) (string, bool, error) {
	row, err := s.read().GetAccount(ctx, accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("reading account %s: %w", accountID, err)
	}

	return row.SecretRef.String, sentToNode(row), nil
}

// AccountsUsingSecretRef names the accounts whose tokens are the secret ref
// names, so the vault can refuse to delete it.
func (s *Service) AccountsUsingSecretRef(ctx context.Context, ref string) ([]string, error) {
	rows, err := s.read().ListAccountLabelsBySecretRef(ctx, nullString(ref))
	if err != nil {
		return nil, fmt.Errorf("listing the accounts that use %q: %w", ref, err)
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, "account "+r.Runtime+"/"+r.Label)
	}

	return out, nil
}

// CoolAccount keeps an account out of use until a usage limit ends, as a
// rate-limited Dispatch does for the account it ran under. A cooldown already
// set to end later stays.
func (s *Service) CoolAccount(ctx context.Context, runtime, id string, until time.Time, reason string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if err := q.UpsertCooldown(ctx, store.UpsertCooldownParams{
			Runtime: runtime, AccountRef: id, Until: until.UnixMilli(), Reason: reason,
		}); err != nil {
			return fmt.Errorf("cooling account %s down: %w", id, err)
		}

		return nil
	})
}
