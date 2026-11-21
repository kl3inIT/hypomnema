package inboxstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// The "log in again" items (issue #1023, section 6; the owner's plan of 2026-11-21, which no reference has in this
// form): when a read of one of the owner's accounts is refused for its sign-in (401 or 403, or a messaging account
// that must be linked again), the Bot reading it adds one open item "Log in again: <account>", given to the owner,
// or keeps the one that is open; the first read of that account that works closes it, with that read as evidence.
// One item per account, never one per failure: the item holds the account's key while it is open (the idempotency
// key, which is unique), and lets it go when it closes, so that the account's next refusal adds a new one; reopened,
// it takes the key again, or is refused while another item of the account is open.

// signInHash marks the key as an account's sign-in item's, never a create's request hash (requestHash's sha256).
const signInHash = "signin"

// SignInKey is the key an account's open "log in again" item holds.
func SignInKey(account string) string { return "hypomnema:signin:" + account }

// closedSignInPrefix starts the key a closed "log in again" item keeps, its own: it names the item and the account,
// so that a reopen knows which account's key to take again.
const closedSignInPrefix = "hypomnema:signin-closed:"

// release lets an account's key go from the item id, which keeps a key of its own.
func release(ctx context.Context, q *store.Queries, id, account string) error {
	return q.SetTodoIdempotencyKey(ctx, store.SetTodoIdempotencyKeyParams{
		IdempotencyKey: nullString(closedSignInPrefix + id + ":" + account), ID: id,
	})
}

// retake gives a "log in again" item being reopened its account's key again (review of #1037): refused while
// another item of the account is open, so that the account never has two. Any other to-do is left as it is.
func retake(ctx context.Context, q *store.Queries, cur store.Todo) error {
	if cur.IdempotencyHash.String != signInHash {
		return nil
	}

	rest, released := strings.CutPrefix(cur.IdempotencyKey.String, closedSignInPrefix)
	if !released {
		return nil // It still holds the account's key.
	}

	_, account, _ := strings.Cut(rest, ":")

	holder, err := signInItem(ctx, q, account)
	switch {
	case err != nil:
		return err
	case holder != "":
		return fmt.Errorf("%w: %s is open for %s: close it before reopening this one", storeerr.ErrConflict, holder,
			account)
	}

	return q.SetTodoIdempotencyKey(ctx, store.SetTodoIdempotencyKeyParams{
		IdempotencyKey: nullString(SignInKey(account)), ID: cur.ID,
	})
}

// SignInTitle is the title of an account's "log in again" item.
func SignInTitle(account string) string { return "Log in again: " + account }

// SignInFailed adds, as the Bot whose read of account in the Routine's run runID was refused, the open "log in
// again" item of that account, given to the owner, with why as its quote; or keeps the one that is open, unchanged.
// added says it was added now.
func (s *Store) SignInFailed(ctx context.Context, bot Actor, account, runID, why string) (t Todo, added bool,
	err error,
) {
	if err := checkSignIn(bot, account, runID); err != nil {
		return Todo{}, false, err
	}

	why = strings.TrimSpace(why)
	if utf8.RuneCountInString(why) > MaxQuote {
		why = string([]rune(why)[:MaxQuote])
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		open, err := signInItem(ctx, q, account)
		if err != nil {
			return err
		}

		if open != "" {
			t, err = s.get(ctx, q, open)

			return err
		}

		id := s.newID("tdo")
		if err := q.InsertTodo(ctx, store.InsertTodoParams{
			ID: id, Title: SignInTitle(account), DueKind: DueNone, AssigneeKind: Me, State: Open,
			Notes: "A read of " + account + " was refused for its sign-in. Sign in to it again; the next read " +
				"that works closes this.",
			SourceKind: FromRoutine, SourceID: runID, SourceQuote: why,
			CreatedByKind: bot.Kind, CreatedBy: bot.ID,
			IdempotencyKey: nullString(SignInKey(account)), IdempotencyHash: nullString(signInHash), Now: now,
		}); err != nil {
			return fmt.Errorf("adding the sign-in item of %s: %w", account, err)
		}

		row, err := q.GetTodo(ctx, id)
		if err != nil {
			return fmt.Errorf("reading the to-do added: %w", err)
		}

		if err := addEvent(ctx, q, row.ID, now, 1, bot, EventCreated, map[string]any{}, fields(row)); err != nil {
			return err
		}

		added = true
		t, err = s.get(ctx, q, id)

		return err
	})
	if err != nil {
		return Todo{}, false, err
	}

	return t, added, nil
}

// SignInWorks closes, as the Bot whose read of account in the Routine's run runID worked, the open "log in again"
// item of that account: done by the owner, who signed in, with the read as evidence. closed says there was one to
// close; an account with none changes nothing.
func (s *Store) SignInWorks(ctx context.Context, bot Actor, account, runID, read string) (t Todo, closed bool,
	err error,
) {
	if err := checkSignIn(bot, account, runID); err != nil {
		return Todo{}, false, err
	}

	evidence, err := json.Marshal([]Link{{Kind: LinkRun, Ref: runID, Label: truncate(read, MaxLabel)}})
	if err != nil {
		return Todo{}, false, fmt.Errorf("writing the evidence: %w", err)
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		open, err := signInItem(ctx, q, account)
		if err != nil || open == "" {
			return err
		}

		cur, err := q.GetTodo(ctx, open)
		if err != nil {
			return fmt.Errorf("reading to-do %s: %w", open, err)
		}

		if cur.State != Open {
			// Proposed, doing or review: whoever has it decides.
			return nil
		}

		c := Change{Actor: bot, ID: open, Revision: int(cur.Revision)}
		if t, err = s.changeTx(ctx, q, now, c, EventState,
			func(_ *store.Queries, _ store.Todo, next *store.Todo, now int64) (map[string]any, error) {
				next.State = Done
				next.DoneByKind, next.DoneBy = nullString(Me), sql.NullString{}
				closeFields(next, bot, ClosedByBot, "A read of "+account+" worked again.", string(evidence), now)

				return map[string]any{"account": account}, nil
			}); err != nil {
			return err
		}

		closed = true

		return release(ctx, q, open, account)
	})
	if err != nil {
		return Todo{}, false, err
	}

	return t, closed, nil
}

// signInItem is the id of an account's open "log in again" item, or empty. One closed by anyone meanwhile lets its
// key go here; a key a Bot gave an item of its own is not taken.
func signInItem(ctx context.Context, q *store.Queries, account string) (string, error) {
	row, err := q.GetTodoByIdempotencyKey(ctx, nullString(SignInKey(account)))

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading the sign-in item of %s: %w", account, err)
	case row.IdempotencyHash.String != signInHash:
		return "", fmt.Errorf("%w: the key of %s's sign-in item is another to-do's", storeerr.ErrConflict, account)
	case Closed(row.State):
		return "", release(ctx, q, row.ID, account)
	}

	return row.ID, nil
}

func checkSignIn(bot Actor, account, runID string) error {
	switch {
	case bot.Kind != Bot || bot.ID == "":
		return fmt.Errorf("%w: a Bot reading the account keeps its sign-in item", storeerr.ErrInvalid)
	case account == "" || len(account) > MaxID:
		return fmt.Errorf("%w: an account is 1 to %d characters", storeerr.ErrInvalid, MaxID)
	case runID == "" || len(runID) > MaxID:
		return fmt.Errorf("%w: a sign-in item comes from a Routine's run", storeerr.ErrInvalid)
	}

	return nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}

	return string([]rune(s)[:n])
}
