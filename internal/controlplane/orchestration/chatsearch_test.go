package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

// searched are the texts of what a search from chatID found.
func searched(t *testing.T, s *Service, chatID, query string) []string {
	t.Helper()

	hits, err := s.SearchChats(t.Context(), chatID, query, 0)
	require.NoError(t, err, query)

	out := []string{}
	for _, h := range hits {
		out = append(out, h.Excerpt)
	}

	return out
}

// Scenario: issue #805, rows 2 and 6. What is typed is searched as words: quotes, a star, NEAR and a minus are never
// FTS5's syntax, so a query with them finds the message that has the words and none fails. A Bot's search reads its
// chats with the owner, never a Routine's run; a Lark group's chat is searched only from that group.
func TestAChatSearchIsOfWordsInTheBotsOwnChats(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	vmap, err := s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	main, err := s.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = s.AddChatMessage(ctx, main.ID, ChatOwner, `The "near" view of the churn-rate dashboard: -10% NEAR(Hanoi)`)
	require.NoError(t, err)

	here, err := s.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)

	for _, query := range []string{
		`"churn`, `churn*`, `NEAR(churn, dashboard)`, `-churn -rate`, `churn AND dashboard`, `"near" view`,
		`10 hanoi`, `churn:rate`, `{dashboard}^`,
	} {
		assert.Len(t, searched(t, s, here.ID, query), 1, query)
	}

	for _, query := range []string{`*`, `"`, `-`, `()`, `churn cohort`} {
		assert.Empty(t, searched(t, s, here.ID, query), "no words, or not every word: %s", query)
	}

	routine, err := s.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: vmap.ID, Name: "digest", Kind: routinestore.RoutinePrompt, Prompt: "Read the churn dashboard.",
		EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	run, err := s.RunRoutineNow(ctx, routine.ID)
	require.NoError(t, err)
	_, err = s.AddChatMessage(ctx, run.Run.ChatID, ChatCoordinator, "The churn dashboard rose.")
	require.NoError(t, err)

	_, err = s.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "maps", AppID: "cli_m4p5", Secret: "lark-channel-maps", Bot: vmap.ID,
		Mode: channelstore.ChannelOwner, OwnerID: "ou_owner", Enabled: true,
	})
	require.NoError(t, err)

	group, err := s.ChannelGroupChatFor(ctx, "maps", "oc_group", "Lark: Maps team")
	require.NoError(t, err)
	_, err = s.AddChatMessage(ctx, group.ChatID, ChatCoordinator, "The churn dashboard for the team.")
	require.NoError(t, err)

	assert.Len(t, searched(t, s, here.ID, "churn dashboard"), 1, "neither a Routine's run nor a Lark group")
	assert.Equal(t, []string{"The churn dashboard for the team."}, searched(t, s, group.ChatID, "churn dashboard"),
		"a Lark group searches only itself")
}

// Scenario: issue #805, row 2. The chats kept before the index are found once it is made; a tool's line and a
// Run's wake-up are not indexed. The way back drops the index and its column.
func TestTheChatsKeptBeforeAreIndexed(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	own := migrationVersion(t, "_chat_search.sql")
	db, p := databaseAt(t, own-1)

	for _, stmt := range []string{
		`INSERT INTO chats (id, title, bot_id, created_at, updated_at) VALUES ('cht_a', '', 'coordinator', 1, 1)`,
		`INSERT INTO chat_messages (chat_id, role, text, created_at) VALUES
			('cht_a', 'owner', 'The revenue dashboard counts paid orders.', 1),
			('cht_a', 'coordinator', 'Noted: revenue is paid orders.', 2),
			('cht_a', 'tool', 'ran revenue_query', 3),
			('cht_a', 'system', 'Task t1 about revenue is succeeded', 4)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	_, err := p.UpTo(ctx, own)
	require.NoError(t, err)

	var found []string

	rows, err := db.QueryContext(ctx, `SELECT text FROM chat_messages_fts WHERE chat_messages_fts = '"revenue"'
		ORDER BY rowid`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var text string
		require.NoError(t, rows.Scan(&text))
		found = append(found, text)
	}

	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"The revenue dashboard counts paid orders.", "Noted: revenue is paid orders."}, found)

	_, err = p.DownTo(ctx, own-1)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name LIKE 'chat_messages_fts%'`).
		Scan(&n))
	assert.Zero(t, n, "the index and its triggers are gone")
}
