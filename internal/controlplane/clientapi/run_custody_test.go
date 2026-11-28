package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

func TestAnOwnerRunKeepsItsChatAcrossACreateRetry(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("laptop", bearertoken.Owner)
	other, _ := w.token("other", bearertoken.Coordinator)
	params := &openapi.CreateRunParams{IdempotencyKey: ptr("retry")}
	body := openapi.NewRun{Objective: "the owner can close the laptop"}

	first, err := owner.CreateRunWithResponse(ctx, params, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, first.StatusCode(), string(first.Body))
	run := first.JSON201
	require.NotEmpty(t, val(run.ChatId))
	assert.Equal(t, "laptop", run.CreatedBy)
	main, err := w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, main.MainChat, val(run.ChatId))
	token, err := w.svc.GetToken(ctx, val(run.CoordinatorTokenId))
	require.NoError(t, err)
	assert.Equal(t, testRunCoordinator, token.Name)

	fresh, err := w.svc.StartMainChat(ctx, "", "", "new main chat", orchestration.ChatModel{})
	require.NoError(t, err)
	again, err := owner.CreateRunWithResponse(ctx, params, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, again.StatusCode(), string(again.Body))
	assert.Equal(t, run.Id, again.JSON201.Id)
	assert.Equal(t, fresh.ID, val(again.JSON201.ChatId), "the open Run moved with the main chat (issue #1169)")
	next := w.createRun(owner, "next work")
	assert.Equal(t, fresh.ID, val(next.ChatId))

	body.ChatId = run.ChatId
	named, err := owner.CreateRunWithResponse(ctx, nil, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, named.StatusCode(), string(named.Body))
	assert.Equal(t, run.ChatId, named.JSON201.ChatId)
	refused, err := other.CreateRunWithResponse(ctx, nil, body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())
	body.ChatId = ptr("chat_missing")
	missing, err := owner.CreateRunWithResponse(ctx, nil, body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())
}

func TestAnInteractiveRunNeedsAWorkingChatCoordinator(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.srv.WithRunCoordinator("missing")
	owner, _ := w.token("owner", bearertoken.Owner)
	resp, err := owner.CreateRunWithResponse(t.Context(), nil, openapi.NewRun{Objective: "needs a chat"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode())
	assert.Contains(t, string(resp.Body), "working coordinator token")
	page, err := owner.ListRunsWithResponse(t.Context(), nil)
	require.NoError(t, err)
	require.NotNil(t, page.JSON200)
	assert.Empty(t, page.JSON200.Data)
}

func TestOnlyTheAssignedBotChatCanContinueAnAdoptedRun(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	other, _ := w.token("other", bearertoken.Coordinator)
	old, err := w.svc.CreateRun(ctx, orchestration.NewRun{CreatedBy: "laptop", Objective: "legacy", Repo: "kl3inIT/demo"})
	require.NoError(t, err)
	question := w.askFromAWorker(owner, old.ID, "which port?", orchestration.AddressCoordinator)
	chat, _, err := w.svc.OpenMainChat(ctx, "", "", "", orchestration.ChatModel{})
	require.NoError(t, err)
	elsewhere, err := w.svc.CreateChat(ctx, chat.Bot, "another chat")
	require.NoError(t, err)
	bot := w.piToken(testRunCoordinator, chat.ID, chat.Bot)
	wrongChat := w.piToken(testRunCoordinator, elsewhere.ID, chat.Bot)
	answer := openapi.Answer{Text: ptr("8080")}

	denied, err := bot.AnswerQuestionWithResponse(ctx, question, answer)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, denied.StatusCode())
	notOwner, err := bot.AdoptRunWithResponse(ctx, old.ID, openapi.RunAdoption{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, notOwner.StatusCode())
	adopted, err := owner.AdoptRunWithResponse(ctx, old.ID, openapi.RunAdoption{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, adopted.StatusCode(), string(adopted.Body))
	assert.Equal(t, chat.ID, val(adopted.JSON200.ChatId))
	assert.Equal(t, "laptop", adopted.JSON200.CreatedBy)

	for _, stranger := range []*openapi.ClientWithResponses{wrongChat, other} {
		denied, err := stranger.AnswerQuestionWithResponse(ctx, question, answer)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, denied.StatusCode())
	}
	answered, err := bot.AnswerQuestionWithResponse(ctx, question, answer)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, answered.StatusCode(), string(answered.Body))
	wk := w.workers[question]
	require.NoError(t, w.svc.AckAnswer(ctx, wk.next(), question))
	ownerGate, err := w.svc.AskQuestion(ctx, wk.next(), orchestration.Ask{To: orchestration.AddressOwner, Text: "ship?"})
	require.NoError(t, err)
	denied, err = bot.AnswerQuestionWithResponse(ctx, ownerGate, answer)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, denied.StatusCode())
	answered, err = owner.AnswerQuestionWithResponse(ctx, ownerGate, answer)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, answered.StatusCode(), string(answered.Body))
	require.NoError(t, w.svc.AckAnswer(ctx, wk.next(), ownerGate))
	require.NoError(t, w.svc.ReportFinished(ctx, wk.next(), orchestration.Outcome{
		Result: orchestration.Result{Summary: "implemented", Exit: orchestration.Exit{Kind: "exited", Code: ptr(0)}},
	}))

	tasks, err := w.svc.ListTasks(ctx, old.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	author := tasks[0].ID
	review := openapi.NewTasks{Tasks: []openapi.NewTask{
		{Ref: "review", Spec: "check the change", Profile: ptr("reviewer"), ReviewOf: &author},
	}}
	follow := openapi.NewFollowUp{Note: "address the review findings"}
	for _, stranger := range []*openapi.ClientWithResponses{wrongChat, other} {
		deniedReview, err := stranger.CreateTasksWithResponse(ctx, old.ID, nil, review)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, deniedReview.StatusCode())
		deniedFollow, err := stranger.FollowUpTaskWithResponse(ctx, author, follow)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, deniedFollow.StatusCode())
	}
	otherRun := w.createRun(owner, "another Run in the same chat")
	foreign, err := bot.CreateTasksWithResponse(ctx, otherRun.Id, nil, review)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, foreign.StatusCode(), string(foreign.Body))
	created, err := bot.CreateTasksWithResponse(ctx, old.ID, nil, review)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(), string(created.Body))
	followed, err := bot.FollowUpTaskWithResponse(ctx, author, follow)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, followed.StatusCode(), string(followed.Body))
}

// Scenario: issue #1334. The owner gives a project Bot its repository; the main Bot's pi, the main Bot's chat
// coordinator, cannot. The main Bot's pi creating a Run on it with no chat gets the Run in the project Bot's main
// chat, routed by repo_owner, and may not plan it there; the project Bot's pi does. Naming its own chat keeps a Run;
// a second Bot's claim on the repository is refused with the holder's name; the owner's Run on a repository no Bot
// owns goes to the main Bot, routed by default.
func TestARunGoesToTheBotThatOwnsItsRepository(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	main, _, err := w.svc.OpenMainChat(ctx, "", "", "", orchestration.ChatModel{})
	require.NoError(t, err)
	beyond, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "BeyondPilot"})
	require.NoError(t, err)
	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "VMAP"})
	require.NoError(t, err)
	hypo := w.piToken(testRunCoordinator, main.ID, orchestration.DefaultBotID)
	claim := openapi.BotRepos{Repos: []string{"acme/beyondpilot"}}

	denied, err := hypo.SetBotReposWithResponse(ctx, beyond.ID, claim)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, denied.StatusCode(), "only the owner sets a Bot's repositories")
	set, err := owner.SetBotReposWithResponse(ctx, beyond.ID, claim)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode(), string(set.Body))
	assert.Equal(t, []string{"acme/beyondpilot"}, val(set.JSON200.Repos))
	taken, err := owner.SetBotReposWithResponse(ctx, vmap.ID, claim)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, taken.StatusCode())
	assert.Contains(t, string(taken.Body), "BeyondPilot")

	routed, err := hypo.CreateRunWithResponse(ctx, nil, openapi.NewRun{
		Objective: "Fix the pilot's report", Repo: ptr("acme/beyondpilot"),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, routed.StatusCode(), string(routed.Body))
	run := routed.JSON201
	owned, err := w.svc.GetBot(ctx, beyond.ID)
	require.NoError(t, err)
	assert.Equal(t, owned.MainChat, val(run.ChatId))
	assert.Equal(t, beyond.ID, val(run.Bot))
	assert.Equal(t, openapi.RoutedByRepoOwner, val(run.RoutedBy))

	tasks := openapi.NewTasks{Tasks: []openapi.NewTask{{Ref: "fix", Spec: "fix the report"}}}
	refused, err := hypo.CreateTasksWithResponse(ctx, run.Id, nil, tasks)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "the Run is the owning Bot's to plan")
	planned, err := w.piToken(testRunCoordinator, owned.MainChat, beyond.ID).CreateTasksWithResponse(ctx, run.Id, nil,
		tasks)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, planned.StatusCode(), string(planned.Body))

	kept, err := hypo.CreateRunWithResponse(ctx, nil, openapi.NewRun{
		Objective: "Look here", Repo: ptr("acme/beyondpilot"), ChatId: ptr(main.ID),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, kept.StatusCode(), string(kept.Body))
	assert.Equal(t, main.ID, val(kept.JSON201.ChatId), "a chat named is kept")
	assert.Equal(t, openapi.RoutedByChat, val(kept.JSON201.RoutedBy))

	unowned, err := owner.CreateRunWithResponse(ctx, nil, openapi.NewRun{
		Objective: "Fix the tool", Repo: ptr("acme/tooling"),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, unowned.StatusCode(), string(unowned.Body))
	assert.Equal(t, main.ID, val(unowned.JSON201.ChatId))
	assert.Equal(t, openapi.RoutedByDefault, val(unowned.JSON201.RoutedBy))

	repos, err := owner.ListBotsWithResponse(ctx)
	require.NoError(t, err)
	require.NotNil(t, repos.JSON200)
	for _, b := range repos.JSON200.Data {
		if b.Id == beyond.ID {
			assert.Equal(t, []string{"acme/beyondpilot"}, val(b.Repos), "the Bots list carries each one's repositories")
		}
	}
}
