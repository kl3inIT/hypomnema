package inboxstore_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// Scenario: issue #1023, section 6: the Thư ký Bot's reads of the owner's Lark are refused twice for its sign-in,
// in two runs; his Inbox has one "Log in again: lark:me" item, given to him, not two. He signs in again; the next
// read that works closes it, done by him, with that read as evidence, and a later refusal adds a new item.
func TestOneSignInItemPerBrokenAccount(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	kai := bot(w.kai)

	first, added, err := w.inbox.SignInFailed(ctx, kai, "lark:me", "rrn_1", "HTTP 401 from open.larksuite.com")
	require.NoError(t, err)
	assert.True(t, added)
	assert.Equal(t, inboxstore.Open, first.State)
	assert.Equal(t, "Log in again: lark:me", first.Title)
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.Me}, first.Assignee)
	assert.Equal(t, "HTTP 401 from open.larksuite.com", first.Source.Quote)

	again, added, err := w.inbox.SignInFailed(ctx, kai, "lark:me", "rrn_2", "HTTP 401 from open.larksuite.com")
	require.NoError(t, err)
	assert.False(t, added, "a second refusal keeps the item")
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.Revision, again.Revision, "and changes nothing of it")

	other, added, err := w.inbox.SignInFailed(ctx, kai, "zalo:main", "rrn_2", "zalo:main must be linked again")
	require.NoError(t, err)
	assert.True(t, added, "another account has its own")

	open, err := w.inbox.List(ctx, inboxstore.Filter{States: []string{inboxstore.Open}}, "", 0)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{first.ID, other.ID}, idsOf(open.Items))

	_, closed, err := w.inbox.SignInWorks(ctx, kai, "google:me", "rrn_3", "google:me was read")
	require.NoError(t, err)
	assert.False(t, closed, "an account with no item changes nothing")

	done, closed, err := w.inbox.SignInWorks(ctx, kai, "lark:me", "rrn_3", "lark:me was read: HTTP 200")
	require.NoError(t, err)
	assert.True(t, closed)
	assert.Equal(t, inboxstore.Done, done.State)
	assert.Equal(t, &inboxstore.Assignee{Kind: inboxstore.Me}, done.DoneBy)
	assert.Equal(t, inboxstore.ClosedByBot, done.ClosedHow)
	assert.Equal(t, []inboxstore.Link{{Kind: inboxstore.LinkRun, Ref: "rrn_3", Label: "lark:me was read: HTTP 200"}},
		done.Evidence)

	_, closed, err = w.inbox.SignInWorks(ctx, kai, "lark:me", "rrn_4", "lark:me was read: HTTP 200")
	require.NoError(t, err)
	assert.False(t, closed, "it is closed once")

	next, added, err := w.inbox.SignInFailed(ctx, kai, "lark:me", "rrn_5", "HTTP 403 from open.larksuite.com")
	require.NoError(t, err)
	assert.True(t, added, "a refusal after it closed adds a new item")
	assert.NotEqual(t, first.ID, next.ID)

	// The owner closes it by hand; the account's next refusal still adds one.
	_, err = w.inbox.Done(ctx, inboxstore.Change{Actor: inboxstore.Actor{Kind: inboxstore.Owner}, ID: next.ID,
		Revision: next.Revision}, inboxstore.Closing{DoneBy: inboxstore.Assignee{Kind: inboxstore.Me},
		How: inboxstore.ClosedByHand})
	require.NoError(t, err)

	last, added, err := w.inbox.SignInFailed(ctx, kai, "lark:me", "rrn_6", "HTTP 401 from open.larksuite.com")
	require.NoError(t, err)
	assert.True(t, added)
	assert.NotEqual(t, next.ID, last.ID)
}

// Scenario: review of #1037, round 2: the owner reopens an old "Log in again: lark:me" item. While another one is
// open for the account, the reopen is refused: never two open items for one account. With none open, the reopened
// item takes the account's key again: the next refusal keeps it, and the next read that works closes it.
func TestAReopenedSignInItemTakesItsAccountsKeyAgain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	kai := bot(w.kai)
	owner := inboxstore.Actor{Kind: inboxstore.Owner}

	old, _, err := w.inbox.SignInFailed(ctx, kai, "lark:me", "rrn_1", "HTTP 401 from open.larksuite.com")
	require.NoError(t, err)
	closed, _, err := w.inbox.SignInWorks(ctx, kai, "lark:me", "rrn_2", "lark:me was read")
	require.NoError(t, err)
	current, added, err := w.inbox.SignInFailed(ctx, kai, "lark:me", "rrn_3", "HTTP 401 from open.larksuite.com")
	require.NoError(t, err)
	require.True(t, added)

	_, err = w.inbox.Reopen(ctx, inboxstore.Change{Actor: owner, ID: old.ID, Revision: closed.Revision})
	require.ErrorIs(t, err, storeerr.ErrConflict, "another item of the account is open")

	open, err := w.inbox.List(ctx, inboxstore.Filter{States: []string{inboxstore.Open}}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{current.ID}, idsOf(open.Items))

	done, _, err := w.inbox.SignInWorks(ctx, kai, "lark:me", "rrn_4", "lark:me was read")
	require.NoError(t, err)

	reopened, err := w.inbox.Reopen(ctx, inboxstore.Change{Actor: owner, ID: old.ID, Revision: closed.Revision})
	require.NoError(t, err, "none is open now")
	assert.Equal(t, inboxstore.Open, reopened.State)
	_ = done

	kept, added, err := w.inbox.SignInFailed(ctx, kai, "lark:me", "rrn_5", "HTTP 401 from open.larksuite.com")
	require.NoError(t, err)
	assert.False(t, added, "the reopened item holds the account's key")
	assert.Equal(t, old.ID, kept.ID)

	_, closedNow, err := w.inbox.SignInWorks(ctx, kai, "lark:me", "rrn_6", "lark:me was read")
	require.NoError(t, err)
	assert.True(t, closedNow)

	open, err = w.inbox.List(ctx, inboxstore.Filter{States: []string{inboxstore.Open}}, "", 0)
	require.NoError(t, err)
	assert.Empty(t, open.Items)
}
