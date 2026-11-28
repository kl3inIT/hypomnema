package coordinator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

var inboxOwner = inboxstore.Actor{Kind: inboxstore.Owner}

// recallWorld is a Bot, vmap, whose main chat holds what the owner agreed about its revenue dashboard.
func recallWorld(t *testing.T) (*orchestration.Service, orchestration.Bot, orchestration.Chat) {
	t.Helper()

	svc, _ := newStore(t)
	ctx := t.Context()

	vmap, err := svc.PutBot(ctx, orchestration.Bot{
		Name:      "vmap",
		AgentSpec: orchestration.AgentSpec{Instructions: "You answer about VMAP."},
	})
	require.NoError(t, err)

	main, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = svc.SetMainChat(ctx, vmap.ID, main.ID)
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, main.ID, orchestration.ChatOwner,
		"The revenue dashboard counts only paid orders, as Lan agreed.")
	require.NoError(t, err)
	_, err = svc.AddChatMessage(ctx, main.ID, orchestration.ChatCoordinator,
		"Noted: the revenue dashboard is paid orders only, refunds left out.")
	require.NoError(t, err)

	return svc, vmap, main
}

// recalled is the block a prompt opens with, empty when it opens with none.
func recalled(prompt string) string {
	if !strings.HasPrefix(prompt, orchestration.RecallOpen) {
		return ""
	}

	block, _, _ := strings.Cut(prompt, orchestration.RecallClose)

	return block
}

// Scenario: issue #805, the design's row 4. The owner discussed the revenue dashboard with Bot vmap in its main
// chat, then hands it an Inbox item about the dashboard and comments on it. Each turn reaches the Bot in the item's
// own chat with the main chat's passages fenced before his words; the item's own chat, which shares the words, is
// never recalled.
func TestAnOwnersCommentOnAnItemRecallsTheBotsMainChat(t *testing.T) {
	t.Parallel()

	svc, vmap, _ := recallWorld(t)
	ctx := t.Context()

	td, err := svc.Inbox().Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Check the revenue dashboard", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)

	td, err = svc.Inbox().Hand(ctx, inboxstore.Change{Actor: inboxOwner, ID: td.ID, Revision: td.Revision}, vmap.ID,
		"Look at its numbers this week.")
	require.NoError(t, err)
	require.NotEmpty(t, td.Chat)

	next := scripted(t, svc, td.Chat)

	handed := next()
	block := recalled(handed)
	require.NotEmpty(t, block, handed)
	assert.Contains(t, block, "your main chat")
	assert.Contains(t, block, "the owner: The revenue dashboard counts only paid orders")
	assert.Contains(t, handed, "<owner_prompt>\nLook at its numbers this week.\n</owner_prompt>",
		"the wake-up follows the block")

	_, err = svc.Inbox().Comment(ctx, inboxOwner, td.ID, "Does the revenue dashboard count refunds too?")
	require.NoError(t, err)

	commented := next()
	block = recalled(commented)
	require.NotEmpty(t, block, commented)
	assert.Contains(t, block, "you: Noted: the revenue dashboard is paid orders only, refunds left out.")
	assert.NotContains(t, block, "Inbox:", "the item's own chat is not recalled")
	assert.NotContains(t, block, "this week", "the item's own chat is not recalled")

	after := strings.TrimPrefix(commented, block+orchestration.RecallClose+"\n\n")
	assert.True(t, strings.HasPrefix(after, "Message from the Hypomnema control plane: these need you."), after)
	assert.Contains(t, after, "<owner_comment>\nDoes the revenue dashboard count refunds too?\n</owner_comment>")
}

// Scenario: issue #805, the design's row 4: no recall but for the owner's own words. In Bot vmap's other chats, the
// owner's message recalls the main chat; an Inbox item another Bot passed, a Routine's run, the owner's words in that
// run's chat, a Lark group's message and a Run's wake-up recall nothing, though they share its words.
func TestOnlyTheOwnersOwnTurnsRecall(t *testing.T) {
	t.Parallel()

	svc, vmap, _ := recallWorld(t)
	ctx := t.Context()

	other, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, other.ID, orchestration.ChatOwner, "What does the revenue dashboard count?")
	require.NoError(t, err)
	assert.NotEmpty(t, recalled(scripted(t, svc, other.ID)()), "the owner's message recalls")

	_, err = svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "me", Secret: "fixture",
	})
	require.NoError(t, err)

	passed, err := svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Bot, ID: orchestration.DefaultBotID},
		inboxstore.NewTodo{
			Title: "Check the revenue dashboard", State: inboxstore.Proposed,
			Source: inboxstore.Source{
				Kind: inboxstore.FromWhatsApp, ID: "8491@s.whatsapp.net", Account: "me",
				Quote: "Is the revenue dashboard right?",
				Ref:   &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0A1"},
			},
			Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: vmap.ID},
		})
	require.NoError(t, err)
	require.NotEmpty(t, passed.Chat)

	prompt := scripted(t, svc, passed.Chat)()
	assert.Contains(t, prompt, "Check the revenue dashboard")
	assert.Empty(t, recalled(prompt), "an item a Bot passed is no turn of the owner")

	routine, err := svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: vmap.ID, Name: "digest", Kind: routinestore.RoutinePrompt,
		Prompt: "Sum up the revenue dashboard's paid orders.", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	run, err := svc.RunRoutineNow(ctx, routine.ID)
	require.NoError(t, err)
	require.NotEmpty(t, run.Run.ChatID)

	inRun := scripted(t, svc, run.Run.ChatID)
	prompt = inRun()
	assert.Contains(t, prompt, "revenue dashboard")
	assert.Empty(t, recalled(prompt), "a Routine's run")

	_, err = svc.AddChatMessage(ctx, run.Run.ChatID, orchestration.ChatOwner, "Add the revenue dashboard's refunds.")
	require.NoError(t, err)
	assert.Empty(t, recalled(inRun()), "the owner's words in a Routine's chat")

	_, err = svc.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "maps", AppID: "cli_m4p5", Secret: "lark-channel-maps", Bot: vmap.ID,
		Mode: channelstore.ChannelOwner, OwnerID: "ou_owner", Enabled: true,
	})
	require.NoError(t, err)

	group, err := svc.ChannelGroupChatFor(ctx, "maps", "oc_group", "Lark: Maps team")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, group.ChatID, orchestration.ChatOwner, "Is the revenue dashboard paid orders?")
	require.NoError(t, err)
	assert.Empty(t, recalled(scripted(t, svc, group.ChatID)()), "a Lark group's message")

	runChat, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)

	r, err := svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "Fix the revenue dashboard's paid orders", CreatedBy: "pi", ChatID: runChat.ID,
	})
	require.NoError(t, err)

	tasks, err := svc.CreateTasks(ctx, r.ID, []orchestration.NewTask{{Ref: "a", Spec: "the revenue dashboard"}})
	require.NoError(t, err)
	require.NoError(t, svc.CancelTask(ctx, tasks[0].ID))

	woken, err := svc.WakeChats(ctx)
	require.NoError(t, err)
	require.Positive(t, woken)

	prompt = scripted(t, svc, runChat.ID)()
	assert.Contains(t, prompt, "Message from the Hypomnema control plane")
	assert.Empty(t, recalled(prompt), "a wake-up of the control plane")
}

// Scenario: issue #1218, row 1. Bot vmap's recall follows its tool switches, as OpenMausBot recalls nothing for a
// bot whose memory is off (server/index.ts:1152): with the report preset, or with its chats family switched off, the
// owner's message in another chat recalls nothing of the main chat; switched back on, it does.
func TestABotWithoutChatSearchRecallsNothing(t *testing.T) {
	t.Parallel()

	svc, vmap, _ := recallWorld(t)
	ctx := t.Context()

	for name, switches := range map[string]orchestration.Bot{
		"the report preset":        {Preset: "report"},
		"the chats family off":     {ToolsOff: []string{"chats"}},
		"chat_search switched off": {ToolsOff: []string{"chat_search"}},
	} {
		vmap.Preset, vmap.Tools, vmap.ToolsOff = switches.Preset, switches.Tools, switches.ToolsOff
		_, err := svc.PutBot(ctx, vmap)
		require.NoError(t, err)

		other, err := svc.CreateChat(ctx, vmap.ID, "")
		require.NoError(t, err)
		_, err = svc.AddChatMessage(ctx, other.ID, orchestration.ChatOwner, "What does the revenue dashboard count?")
		require.NoError(t, err)

		prompt := scripted(t, svc, other.ID)()
		assert.Contains(t, prompt, "What does the revenue dashboard count?", name)
		assert.Empty(t, recalled(prompt), name)
	}

	vmap.Preset, vmap.Tools, vmap.ToolsOff = "", nil, nil
	_, err := svc.PutBot(ctx, vmap)
	require.NoError(t, err)

	other, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = svc.AddChatMessage(ctx, other.ID, orchestration.ChatOwner, "What does the revenue dashboard count?")
	require.NoError(t, err)
	assert.NotEmpty(t, recalled(scripted(t, svc, other.ID)()), "switched on, it recalls")
}

// Scenario: issue #1218, row 4 (ARCHITECTURE.md's claims). The line that continues a cut turn, a steer the owner
// sends while the Bot works, and his message in a Lark direct chat recall nothing, though each shares the main chat's
// words.
func TestNoRecallOnAContinueLineASteerOrALarkDirectChat(t *testing.T) {
	t.Parallel()

	svc, vmap, _ := recallWorld(t)
	ctx := t.Context()

	cut, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = svc.AddChatMessage(ctx, cut.ID, orchestration.ChatOwner, "What does the revenue dashboard count?")
	require.NoError(t, err)

	next := scripted(t, svc, cut.ID)
	require.NotEmpty(t, recalled(next()), "the owner's message recalls")
	require.NoError(t, svc.ContinueCutTurn(ctx, cut.ID))

	prompt := next()
	assert.Contains(t, prompt, "cut your previous turn")
	assert.Empty(t, recalled(prompt), "the line that continues a cut turn")

	busy, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = svc.AddChatMessage(ctx, busy.ID, orchestration.ChatOwner, "Write the weekly sales note.")
	require.NoError(t, err)

	sup, _, pi := scriptedSupervisor(t, svc, busy.ID)
	require.NoError(t, sup.deliver(ctx, busy.ID))
	require.True(t, sup.Working(busy.ID))

	_, err = svc.AddChatMessage(ctx, busy.ID, orchestration.ChatOwner, "Also, does the revenue dashboard count refunds?")
	require.NoError(t, err)
	require.NoError(t, sup.deliver(ctx, busy.ID))
	require.Len(t, pi.prompts, 2)
	assert.Contains(t, pi.prompts[1], "does the revenue dashboard count refunds?")
	assert.Empty(t, recalled(pi.prompts[1]), "a steer")

	_, err = svc.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "maps", AppID: "cli_m4p5", Secret: "lark-channel-maps", Bot: vmap.ID,
		Mode: channelstore.ChannelOwner, OwnerID: "ou_owner", Enabled: true,
	})
	require.NoError(t, err)

	direct, err := svc.ChannelChatFor(ctx, "maps", "oc_direct", "ou_owner", "Lark: the owner")
	require.NoError(t, err)
	_, err = svc.AddChatMessage(ctx, direct.ChatID, orchestration.ChatOwner, "Is the revenue dashboard paid orders?")
	require.NoError(t, err)
	assert.Empty(t, recalled(scripted(t, svc, direct.ChatID)()), "a Lark direct chat")
}

// Scenario: issue #1218, row 5. pi refuses the owner's turn, and the main chat gains a passage that shares its words
// before the supervisor gives it again: the turn is given with the recall it had, computed once.
func TestATurnGivenAgainKeepsItsRecall(t *testing.T) {
	t.Parallel()

	svc, vmap, main := recallWorld(t)
	ctx := t.Context()

	other, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = svc.AddChatMessage(ctx, other.ID, orchestration.ChatOwner, "What does the revenue dashboard count?")
	require.NoError(t, err)

	sup, _, pi := scriptedSupervisor(t, svc, other.ID)
	pi.refuse = 1

	require.Error(t, sup.deliver(ctx, other.ID), "pi refused the turn")
	require.Len(t, pi.prompts, 1)
	require.NotEmpty(t, recalled(pi.prompts[0]))

	_, err = svc.AddChatMessage(ctx, main.ID, orchestration.ChatOwner,
		"From December the revenue dashboard counts refunds as negative revenue.")
	require.NoError(t, err)

	require.NoError(t, sup.deliver(ctx, other.ID))
	require.Len(t, pi.prompts, 2)
	assert.Equal(t, pi.prompts[0], pi.prompts[1], "the turn is given as it was")
}

// Scenario: review of PR #1227, row 1. pi refuses the owner's turn, then the owner switches vmap's chats family off
// before the supervisor gives the turn again: the turn is given without the recall it had, as a new turn would be.
func TestATurnGivenAgainAfterChatsIsSwitchedOffRecallsNothing(t *testing.T) {
	t.Parallel()

	svc, vmap, _ := recallWorld(t)
	ctx := t.Context()

	other, err := svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	_, err = svc.AddChatMessage(ctx, other.ID, orchestration.ChatOwner, "What does the revenue dashboard count?")
	require.NoError(t, err)

	sup, _, pi := scriptedSupervisor(t, svc, other.ID)
	pi.refuse = 1

	require.Error(t, sup.deliver(ctx, other.ID), "pi refused the turn")
	require.NotEmpty(t, recalled(pi.prompts[0]))

	vmap.ToolsOff = []string{"chats"}
	_, err = svc.PutBot(ctx, vmap)
	require.NoError(t, err)

	require.NoError(t, sup.deliver(ctx, other.ID))
	require.Len(t, pi.prompts, 2)
	assert.Contains(t, pi.prompts[1], "What does the revenue dashboard count?")
	assert.Empty(t, recalled(pi.prompts[1]), "switched off, the kept recall is not given")
}
