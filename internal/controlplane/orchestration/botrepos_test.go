package orchestration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// repoWorld is the main Bot with its main chat, a project Bot that owns a repository (issue #1334), and the
// control plane's chat coordinator token.
type repoWorld struct {
	*world
	beyond Bot
	main   Chat
	parent Token
}

func newRepoWorld(t *testing.T) repoWorld {
	t.Helper()

	w := newSeedWorld(t)
	ctx := t.Context()

	parent, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	main, _, err := w.s.OpenMainChat(ctx, "", "", "", ChatModel{})
	require.NoError(t, err)

	beyond, err := w.s.PutBot(ctx, Bot{Name: "BeyondPilot", Persona: "The BeyondPilot project."})
	require.NoError(t, err)

	beyond, err = w.s.SetBotRepos(ctx, beyond.ID, []string{"acme/beyondpilot"})
	require.NoError(t, err)

	return repoWorld{world: w, beyond: beyond, main: main, parent: parent}
}

// fromBot is a Run created by a Bot's pi in its chat, as the client API makes it from the pi's token.
func (w repoWorld) fromBot(bot, chat, repo string, named bool) (Run, error) {
	return w.s.CreateRunOnce(w.t.Context(), Once{}, NewRun{
		Objective: "Fix the pilot's report", CreatedBy: "pi", Repo: repo, ChatID: chat, ChatNamed: named, Bot: bot,
		CoordinatorTokenID: w.parent.ID,
	})
}

// Scenario: issue #1334, decisions 1 and 2. The owner gives a project Bot its repository: the Bot lists it, and
// `repos list` shows the Bot. A second Bot's claim on it, in any spelling, is refused with the holder's name and
// changes nothing; the main Bot owns none. Removing the Bot frees the repository for another.
func TestARepositoryHasOneOwningBot(t *testing.T) {
	t.Parallel()

	w := newRepoWorld(t)
	ctx := t.Context()

	assert.Equal(t, []string{"acme/beyondpilot"}, w.beyond.Repos)

	_, err := w.s.AddRepo(ctx, "beyondpilot", "acme/BeyondPilot", "", "", "", "")
	require.NoError(t, err)

	repos, err := w.s.ListRepos(ctx)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, w.beyond.ID, repos[0].Bot, "the registered repository names the Bot that owns it")

	vmap, err := w.s.PutBot(ctx, Bot{Name: "VMAP"})
	require.NoError(t, err)

	for _, claim := range []string{"acme/beyondpilot", "https://github.com/ACME/BeyondPilot", "beyondpilot"} {
		_, err = w.s.SetBotRepos(ctx, vmap.ID, []string{"acme/vmap", claim})
		require.ErrorIs(t, err, storeerr.ErrConflict, claim)
		assert.Contains(t, err.Error(), "BeyondPilot", "the refusal names the Bot that holds it")
	}

	got, err := w.s.GetBot(ctx, vmap.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Repos, "a refused claim changes nothing")

	_, err = w.s.SetBotRepos(ctx, DefaultBotID, []string{"acme/other"})
	require.ErrorIs(t, err, storeerr.ErrInvalid, "the main Bot owns no repository")

	_, err = w.s.ForceDeleteBot(ctx, w.beyond.ID)
	require.NoError(t, err)

	vmapNow, err := w.s.SetBotRepos(ctx, vmap.ID, []string{"acme/beyondpilot"})
	require.NoError(t, err, "removing the Bot freed its repository")
	assert.Equal(t, []string{"acme/beyondpilot"}, vmapNow.Repos)

	freed, err := w.s.SetBotRepos(ctx, vmap.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, freed.Repos, "an empty list frees them all")
}

// Scenario: issue #1334, decisions 3 and 5, from the owner's CLI with no --chat. A Run on a repository a Bot owns is
// in that Bot's main chat, routed by repo_owner; with --chat the chat named wins; a repository no Bot owns goes to
// the main Bot's main chat, routed by default.
func TestAnOwnerRunGoesToTheBotThatOwnsItsRepository(t *testing.T) {
	t.Parallel()

	w := newRepoWorld(t)
	ctx := t.Context()

	owned, err := w.s.CreateChatRunOnce(ctx, Once{}, NewRun{
		Objective: "Fix the pilot's report", CreatedBy: "laptop", Repo: "acme/BeyondPilot",
	}, w.parent.Name)
	require.NoError(t, err)

	beyond, err := w.s.GetBot(ctx, w.beyond.ID)
	require.NoError(t, err)
	require.NotEmpty(t, beyond.MainChat, "the owning Bot's main chat is made when it has none")
	assert.Equal(t, beyond.MainChat, owned.ChatID)
	assert.Equal(t, w.beyond.ID, owned.Bot)
	assert.Equal(t, RoutedRepoOwner, owned.RoutedBy)
	assert.Equal(t, w.parent.ID, owned.CoordinatorTokenID)

	named, err := w.s.CreateChatRunOnce(ctx, Once{}, NewRun{
		Objective: "Fix it here", CreatedBy: "laptop", Repo: "acme/beyondpilot", ChatID: w.main.ID, ChatNamed: true,
	}, w.parent.Name)
	require.NoError(t, err)
	assert.Equal(t, w.main.ID, named.ChatID, "the chat named wins")
	assert.Equal(t, RoutedChat, named.RoutedBy)

	unowned, err := w.s.CreateChatRunOnce(ctx, Once{}, NewRun{
		Objective: "Fix the tool", CreatedBy: "laptop", Repo: "acme/tooling",
	}, w.parent.Name)
	require.NoError(t, err)
	assert.Equal(t, w.main.ID, unowned.ChatID)
	assert.Equal(t, DefaultBotID, unowned.Bot)
	assert.Equal(t, RoutedDefault, unowned.RoutedBy)
}

// Scenario: issue #1334, decision 4. The main Bot creates a Run on a repository the project Bot owns: the Run goes to
// the project Bot's main chat, which is woken with it to plan it, and the main Bot's chat gets a line naming the Run
// and that chat. Naming its own chat keeps the Run there. A project Bot's Run on its own repository, or on one no Bot
// owns, stays in its chat.
func TestABotsRunOnARepositoryAnotherBotOwnsGoesToThatBot(t *testing.T) {
	t.Parallel()

	w := newRepoWorld(t)
	ctx := t.Context()

	routed, err := w.fromBot(DefaultBotID, w.main.ID, "acme/beyondpilot", false)
	require.NoError(t, err)

	beyond, err := w.s.GetBot(ctx, w.beyond.ID)
	require.NoError(t, err)
	assert.Equal(t, beyond.MainChat, routed.ChatID)
	assert.Equal(t, w.beyond.ID, routed.Bot)
	assert.Equal(t, RoutedRepoOwner, routed.RoutedBy)
	assert.Equal(t, w.parent.ID, routed.CoordinatorTokenID, "the owning Bot's pi, under the same coordinator, takes it")

	assert.Contains(t, w.lastLine(w.main.ID), "Run "+routed.ID+" went to chat "+beyond.MainChat+" of BeyondPilot")

	notice := w.undelivered(beyond.MainChat)
	require.Len(t, notice, 1)
	assert.Contains(t, notice[0].Text, "created run "+routed.ID+" on acme/beyondpilot from its chat "+w.main.ID)
	assert.Contains(t, notice[0].Text, "hypomnema runs show "+routed.ID)

	kept, err := w.fromBot(DefaultBotID, w.main.ID, "acme/beyondpilot", true)
	require.NoError(t, err)
	assert.Equal(t, w.main.ID, kept.ChatID, "a chat named is kept")
	assert.Equal(t, RoutedChat, kept.RoutedBy)

	own, err := w.fromBot(w.beyond.ID, beyond.MainChat, "acme/beyondpilot", false)
	require.NoError(t, err)
	assert.Equal(t, beyond.MainChat, own.ChatID)
	assert.Equal(t, RoutedOwnChat, own.RoutedBy)

	// Review round 1 of PR #1343: a project Bot that owns a repository does not keep a Run on one no Bot owns; it goes
	// to the main Bot, routed by default, as the issue's row 3 says ("whose Bot owns X or owns nothing").
	unowned, err := w.fromBot(w.beyond.ID, beyond.MainChat, "acme/tooling", false)
	require.NoError(t, err)
	assert.Equal(t, w.main.ID, unowned.ChatID)
	assert.Equal(t, DefaultBotID, unowned.Bot)
	assert.Equal(t, RoutedDefault, unowned.RoutedBy)
	assert.Contains(t, w.lastLine(beyond.MainChat), "Run "+unowned.ID+" went to chat "+w.main.ID)

	// A Bot that owns nothing keeps its Run on a repository no Bot owns.
	vmap, err := w.s.PutBot(ctx, Bot{Name: "VMAP"})
	require.NoError(t, err)
	side, err := w.s.CreateChat(ctx, vmap.ID, "VMAP's chat")
	require.NoError(t, err)

	kept2, err := w.fromBot(vmap.ID, side.ID, "acme/tooling", false)
	require.NoError(t, err)
	assert.Equal(t, side.ID, kept2.ChatID)
	assert.Equal(t, RoutedOwnChat, kept2.RoutedBy)
}

// Scenario: review round 1 of PR #1343. A create retried after the upgrade, with the same request and key as one made
// before it, returns the first Run: the receipt kept before ChatNamed existed still matches the request.
func TestARunCreateRetriedAcrossTheUpgradeReturnsTheFirstRun(t *testing.T) {
	t.Parallel()

	w := newRepoWorld(t)
	ctx := t.Context()
	nr := NewRun{Objective: "Fix the tool", CreatedBy: "laptop", Repo: "acme/tooling"}

	first, err := w.s.CreateChatRunOnce(ctx, Once{}, nr, w.parent.Name)
	require.NoError(t, err)

	// NewRun as main had it before this change, and the receipt its create kept.
	before := struct {
		Objective          string
		CreatedBy          string
		Repo               string
		Base               string
		CoordinatorTokenID string
		ChatID             string
		Uses               []string
		Bot                string
	}{Objective: nr.Objective, CreatedBy: nr.CreatedBy, Repo: nr.Repo}
	hash, err := requestHash("runs.create", before)
	require.NoError(t, err)
	response, err := json.Marshal(first.ID)
	require.NoError(t, err)
	require.NoError(t, store.New(w.db).InsertReceipt(ctx, store.InsertReceiptParams{
		Caller: "tok_laptop", RequestID: "retry", Method: "runs.create", PayloadHash: hash, Response: string(response),
		Now: 1,
	}))

	again, err := w.s.CreateChatRunOnce(ctx, Once{Caller: "tok_laptop", Key: "retry"}, nr, w.parent.Name)
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
}

// Scenario: issue #1334, decision 6. The main Bot, given a to-do no project Bot fits, keeps it and says why: the item
// carries its reason, shown as not passed. A project Bot cannot say it, nor the main Bot on an item it does not have;
// passing the item on clears the reason.
func TestTheMainBotSaysWhyItPassedAToDoToNoBot(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()
	reason := "It needs the client's billing account, and no project Bot has it."

	kept, err := w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(DefaultBotID), ID: w.item.ID,
		Revision: w.item.Revision}, inboxstore.Edit{NotPassed: &reason})
	require.NoError(t, err)
	assert.Equal(t, reason, kept.NotPassed)
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID}, kept.Assignee, "the item stays")

	_, err = w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(w.beyond.ID), ID: kept.ID, Revision: kept.Revision},
		inboxstore.Edit{NotPassed: &reason})
	require.ErrorIs(t, err, storeerr.ErrForbidden, "only the main Bot says it")

	to := inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.beyond.ID}
	_, err = w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(DefaultBotID), ID: kept.ID, Revision: kept.Revision},
		inboxstore.Edit{NotPassed: &reason, Assignee: &to})
	require.ErrorIs(t, err, storeerr.ErrInvalid, "not with a pass")

	passed, err := w.pass(DefaultBotID, kept, to)
	require.NoError(t, err)
	assert.Empty(t, passed.NotPassed, "passing it on clears the reason")

	_, err = w.s.Inbox().Edit(ctx, inboxstore.Change{Actor: asBot(DefaultBotID), ID: passed.ID,
		Revision: passed.Revision}, inboxstore.Edit{NotPassed: &reason})
	require.Error(t, err, "the main Bot says it only of an item it has")
}

// Scenario: issue #1334, decision 7. A pass to a Bot that was removed is refused, and the main Bot's wake-up lists
// only the Bots there are, each with the repositories it owns.
func TestAPassToARemovedBotIsRefused(t *testing.T) {
	t.Parallel()

	w := newPassWorld(t)
	ctx := t.Context()

	_, err := w.s.SetBotRepos(ctx, w.beyond.ID, []string{"acme/beyondpilot"})
	require.NoError(t, err)

	require.NoError(t, w.s.DeleteBot(ctx, w.vmap.ID))

	_, err = w.pass(DefaultBotID, w.item, inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.vmap.ID})
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	other, err := w.s.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Another", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	other, err = w.s.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: other.ID, Revision: other.Revision},
		DefaultBotID, "Do it")
	require.NoError(t, err)

	wakes := w.wakes(other.Chat)
	require.Len(t, wakes, 1)
	assert.NotContains(t, wakes[0], w.vmap.ID, "a removed Bot is not offered")
	assert.Contains(t, wakes[0], "Repositories: acme/beyondpilot.")
	assert.Contains(t, wakes[0], "not_passed")
}
