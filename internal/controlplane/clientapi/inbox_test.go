package clientapi

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// inboxWorld is the client API with the four kinds of caller of issue #966, section 2: the owner; a Bot in its
// chat with the owner; a Bot in a Routine's run (a script's token); and a coordinator token.
type inboxWorld struct {
	*world
	owner, chatBot, routineBot, coordinator *openapi.ClientWithResponses
	// chat is the chat of chatBot, run the live run of routineBot's Routine.
	chat, run string
	// vmap is the Routine's Bot, as an assignee; chatBot is the main Bot.
	vmap openapi.TodoAssignee
	// said is the owner's message in chat, evidence when a Bot records for him.
	said []openapi.TodoLink
}

func newInboxWorld(t *testing.T) *inboxWorld {
	t.Helper()

	return newInboxWorldAt(t, platform.SystemClock{})
}

func newInboxWorldAt(t *testing.T, clock platform.Clock) *inboxWorld {
	t.Helper()

	ctx := t.Context()
	w := newWorldAt(t, clock)
	owner, _ := w.token("owner", bearertoken.Owner)
	coordinator, _ := w.token("pi-coordinator", bearertoken.Coordinator)

	chat, err := w.svc.CreateChat(ctx, orchestration.DefaultBotID, "")
	require.NoError(t, err)

	added, err := w.svc.AddChatMessages(ctx, chat.ID,
		orchestration.NewChatMessage{Role: orchestration.ChatOwner, Text: "Xong rồi"})
	require.NoError(t, err)

	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap"})
	require.NoError(t, err)

	script, text, every := "scan.sh", "#!/bin/sh\n", 60
	made, err := owner.PutRoutineWithResponse(ctx, vmap.ID, "scan",
		openapi.RoutineSettings{Kind: openapi.Script, Script: &script, ScriptText: &text, EveryMinutes: &every})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode(), string(made.Body))

	started, err := w.svc.RunRoutineNow(ctx, made.JSON200.Id)
	require.NoError(t, err)

	secret, err := w.svc.AddRoutineToken(ctx, "pi-coordinator", made.JSON200.Id)
	require.NoError(t, err)

	return &inboxWorld{
		world: w, owner: owner, coordinator: coordinator,
		chatBot: w.piToken("pi-coordinator", chat.ID, orchestration.DefaultBotID), routineBot: w.client(secret),
		chat: chat.ID, run: started.Run.ID, vmap: openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindBot, Name: &vmap.ID},
		said: []openapi.TodoLink{{Kind: openapi.TodoLinkKindChatMessage, Ref: strconv.FormatInt(added[0].Seq, 10)}},
	}
}

// add is an item the owner wrote, given to whom.
func (w *inboxWorld) add(ctx context.Context, t *testing.T, title string, who openapi.TodoAssignee) openapi.Todo {
	t.Helper()

	resp, err := w.owner.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{},
		openapi.NewTodo{Title: title, Assignee: &who})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))

	return *resp.JSON201
}

// proposed is an item the Routine's Bot found.
func (w *inboxWorld) proposed(t *testing.T) openapi.Todo {
	t.Helper()

	resp, err := w.routineBot.CreateTodoWithResponse(t.Context(), &openapi.CreateTodoParams{},
		openapi.NewTodo{Title: "Answer Linh", Source: &openapi.NewTodoSource{Quote: ptr("Anh trả lời giúp em")}})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))

	return *resp.JSON201
}

var (
	toOwner = openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindMe}
	toMain  = openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindBot, Name: ptr(orchestration.DefaultBotID)}
)

func (w *inboxWorld) get(t *testing.T, id string) openapi.Todo {
	t.Helper()

	resp, err := w.owner.GetTodoWithResponse(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))

	return *resp.JSON200
}

// inboxCallerCase is one of the four callers, with the assignee an item it works on is given to: itself for a Bot.
type inboxCallerCase struct {
	name string
	c    *openapi.ClientWithResponses
	bot  openapi.TodoAssignee
}

// Scenario: issue #966, section 2: who acts comes from the token. For every operation, the owner, a Bot in its chat
// with the owner, a Bot in a Routine's run and a coordinator token are allowed or refused as the table says; a
// coordinator reads only. A Routine's Bot records nothing for the owner: it has no chat with him where his message
// would be. An item each adds names its own source: by hand, the token's chat, the Routine's run.
func TestEachTokenActsAsItsCaller(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newInboxWorld(t)
	callers := []inboxCallerCase{
		{"owner", w.owner, toMain}, {"chat bot", w.chatBot, toMain},
		{"routine bot", w.routineBot, w.vmap}, {"coordinator", w.coordinator, toMain},
	}

	type call func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error)

	// Each operation is called on a fresh item that fits it, so a refusal is the caller's, not the item's.
	ops := []struct {
		name string
		item func(t *testing.T, c inboxCallerCase) openapi.Todo
		do   call
		// want is the status for owner, chat Bot, Routine Bot, coordinator.
		want [4]int
	}{
		{"ListTodos", nil, func(c *openapi.ClientWithResponses, _ openapi.Todo) (any, error) {
			return c.ListTodosWithResponse(ctx, &openapi.ListTodosParams{})
		}, [4]int{200, 200, 200, 200}},
		{"GetInboxNeedsYou", nil, func(c *openapi.ClientWithResponses, _ openapi.Todo) (any, error) {
			return c.GetInboxNeedsYouWithResponse(ctx)
		}, [4]int{200, 200, 200, 200}},
		{"GetTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Read", toOwner) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.GetTodoWithResponse(ctx, td.Id)
			}, [4]int{200, 200, 200, 200}},
		{"CreateTodo", nil, func(c *openapi.ClientWithResponses, _ openapi.Todo) (any, error) {
			return c.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{Title: "Pay"})
		}, [4]int{201, 201, 201, 403}},
		{"EditTodo", func(t *testing.T, c inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Edit", c.bot) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.EditTodoWithResponse(ctx, td.Id, openapi.TodoEdit{Revision: td.Revision, Notes: ptr("x")})
			}, [4]int{200, 200, 200, 403}},
		{"AcceptTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo { return w.proposed(t) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.AcceptTodoWithResponse(ctx, td.Id, openapi.TodoChange{Revision: td.Revision})
			}, [4]int{200, 403, 403, 403}},
		{"DoneTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Done", toOwner) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.DoneTodoWithResponse(ctx, td.Id, openapi.TodoDone{Revision: td.Revision, DoneBy: &toOwner,
					Evidence: &w.said})
			}, [4]int{200, 200, 400, 403}},
		{"DropTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Drop", toOwner) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.DropTodoWithResponse(ctx, td.Id, openapi.TodoDrop{Revision: td.Revision, Reason: "No need",
					Evidence: &w.said})
			}, [4]int{200, 200, 400, 403}},
		{"DuplicateTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Twice", toOwner) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.DuplicateTodoWithResponse(ctx, td.Id, openapi.TodoDuplicate{Revision: td.Revision,
					Of: w.add(ctx, t, "Once", toOwner).Id})
			}, [4]int{200, 403, 403, 403}},
		{"ReopenTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo {
			td := w.add(ctx, t, "Reopen", toOwner)
			r, err := w.owner.DoneTodoWithResponse(ctx, td.Id, openapi.TodoDone{Revision: td.Revision})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, r.StatusCode(), string(r.Body))

			return *r.JSON200
		}, func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
			return c.ReopenTodoWithResponse(ctx, td.Id, openapi.TodoChange{Revision: td.Revision})
		}, [4]int{200, 403, 403, 403}},
		{"SnoozeTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Later", toOwner) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				until := time.Now().Add(time.Hour)

				return c.SnoozeTodoWithResponse(ctx, td.Id, openapi.TodoSnooze{Revision: td.Revision, Until: &until})
			}, [4]int{200, 403, 403, 403}},
		{"StartTodo", func(t *testing.T, c inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Start", c.bot) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.StartTodoWithResponse(ctx, td.Id, openapi.TodoChange{Revision: td.Revision})
			}, [4]int{403, 200, 200, 403}},
		{"ReleaseTodo", func(t *testing.T, c inboxCallerCase) openapi.Todo {
			starter := w.chatBot
			if c.bot == w.vmap {
				starter = w.routineBot
			}

			td := w.add(ctx, t, "Release", c.bot)
			r, err := starter.StartTodoWithResponse(ctx, td.Id, openapi.TodoChange{Revision: td.Revision})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, r.StatusCode(), string(r.Body))

			return *r.JSON200
		}, func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
			return c.ReleaseTodoWithResponse(ctx, td.Id, openapi.TodoRelease{Revision: td.Revision,
				Attempt: &td.Work.Attempt, Reason: "Stuck"})
		}, [4]int{403, 200, 200, 403}},
		// Issue #1111: only the owner hands an item to a Bot; a Bot comments on its own item.
		{"HandTodo", func(t *testing.T, _ inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Hand", toOwner) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.HandTodoWithResponse(ctx, td.Id, openapi.TodoHand{Revision: td.Revision, Prompt: ptr("Do it")})
			}, [4]int{200, 403, 403, 403}},
		{"CommentTodo", func(t *testing.T, c inboxCallerCase) openapi.Todo { return w.add(ctx, t, "Say", c.bot) },
			func(c *openapi.ClientWithResponses, td openapi.Todo) (any, error) {
				return c.CommentTodoWithResponse(ctx, td.Id, openapi.TodoComment{Text: "On it"})
			}, [4]int{200, 200, 200, 403}},
	}

	covered := map[string]bool{}

	for _, op := range ops {
		covered[op.name] = true

		for i, c := range callers {
			var td openapi.Todo
			if op.item != nil {
				td = op.item(t, c)
			}

			r, err := op.do(c.c, td)
			require.NoError(t, err)

			code, body := status(r)
			assert.Equal(t, op.want[i], code, "%s by the %s: %s", op.name, c.name, body)
		}
	}

	for id, p := range policies {
		if strings.HasSuffix(id, "Todo") || strings.HasSuffix(id, "Todos") || strings.HasPrefix(id, "GetInbox") {
			assert.True(t, covered[id], "%s is checked for every caller", id)
			assert.Equal(t, strings.HasPrefix(id, "Get") || strings.HasPrefix(id, "List"), p.scope == scopeNone, id)
		}
	}

	// Where each caller's item comes from, and who added it.
	for _, c := range callers[:3] {
		r, err := c.c.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{Title: "From " + c.name})
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, r.StatusCode(), string(r.Body))

		switch c.name {
		case "owner":
			assert.Equal(t, openapi.TodoActor{Kind: openapi.TodoActorKindOwner}, r.JSON201.CreatedBy)
			assert.Equal(t, openapi.TodoSource{Kind: openapi.TodoSourceKindOwner}, r.JSON201.Source)
		case "chat bot":
			assert.Equal(t, openapi.TodoActor{Kind: openapi.TodoActorKindBot, Id: ptr(orchestration.DefaultBotID)},
				r.JSON201.CreatedBy)
			assert.Equal(t, openapi.TodoSource{Kind: openapi.TodoSourceKindChat, Id: &w.chat}, r.JSON201.Source)
			assert.Equal(t, openapi.TodoStateOpen, r.JSON201.State)
		default:
			assert.Equal(t, openapi.TodoActor{Kind: openapi.TodoActorKindBot, Id: w.vmap.Name}, r.JSON201.CreatedBy,
				"the Routine's Bot")
			assert.Equal(t, openapi.TodoSource{Kind: openapi.TodoSourceKindRoutine, Id: &w.run}, r.JSON201.Source)
			assert.Equal(t, openapi.TodoStateProposed, r.JSON201.State)
		}
	}
}

// status is a generated response's status and body.
func status(r any) (int, string) {
	v := reflect.ValueOf(r).Elem()

	return v.FieldByName("HTTPResponse").Interface().(*http.Response).StatusCode, //nolint:forcetypeassert // generated
		string(v.FieldByName("Body").Bytes())
}

// Scenario: the actor and the source in the body are not the caller's to choose: a Bot in its chat that says it is
// the owner adds as itself, from its chat; one that names another chat, or a source its token does not add from,
// is refused.
func TestTheActorInTheBodyIsIgnored(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newInboxWorld(t)

	r, err := w.chatBot.CreateTodoWithBodyWithResponse(ctx, &openapi.CreateTodoParams{}, "application/json",
		strings.NewReader(`{"title": "Renew the passport", "actor": {"kind": "owner"},
			"created_by": {"kind": "owner"}, "recorded_by": {"kind": "owner"}}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, r.StatusCode(), string(r.Body))
	assert.Equal(t, openapi.TodoActor{Kind: openapi.TodoActorKindBot, Id: ptr(orchestration.DefaultBotID)},
		r.JSON201.CreatedBy)
	assert.Equal(t, openapi.TodoSource{Kind: openapi.TodoSourceKindChat, Id: &w.chat}, r.JSON201.Source)

	other := "cht_other"
	named, err := w.chatBot.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{
		Title: "Pay", Source: &openapi.NewTodoSource{Id: &other},
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, named.StatusCode(), "the chat is the token's")

	for _, kind := range []openapi.TodoSourceKind{openapi.TodoSourceKindOwner, openapi.TodoSourceKindRoutine,
		openapi.TodoSourceKindLark} {
		r, err := w.chatBot.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{
			Title: "Pay", Source: &openapi.NewTodoSource{Kind: &kind},
		})
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, r.StatusCode(), "a chat's Bot adds from %s", kind)
	}

	byHand, err := w.owner.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{
		Title: "Pay", Source: &openapi.NewTodoSource{Kind: ptr(openapi.TodoSourceKindChat)},
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, byHand.StatusCode(), "the owner writes by hand")
}

// Scenario: the owner's triage of 2026-11-20 through the API: a Routine's Bot proposes by default; it opens an item
// only with why_open, a quote and a reference, and then why_open is shown and in the created event; a chat at Lark
// is named in the body. The pi of a prompt run's chat acts in the Routine's run, not in a chat with the owner.
func TestARoutineBotOpensOnlyWhatIsClear(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newInboxWorld(t)
	quote := "Anh check giúp em hợp đồng nhé"
	ref := openapi.TodoLink{Kind: openapi.TodoLinkKindLarkMessage, Ref: "om_456"}
	lark := openapi.TodoSourceKindLark
	asked := openapi.TodoWhyOpen(inboxstore.AskedDirectly)
	open := openapi.NewTodoStateOpen

	create := func(n openapi.NewTodo) *openapi.CreateTodoResponse {
		t.Helper()

		r, err := w.routineBot.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, n)
		require.NoError(t, err)

		return r
	}

	proposed := create(openapi.NewTodo{Title: "Check the contract"})
	require.Equal(t, http.StatusCreated, proposed.StatusCode(), string(proposed.Body))
	assert.Equal(t, openapi.TodoStateProposed, proposed.JSON201.State, "a Routine's Bot proposes by default")

	for name, n := range map[string]openapi.NewTodo{
		"without why_open": {Title: "Check", State: &open, Source: &openapi.NewTodoSource{Kind: &lark,
			Id: ptr("oc_1"), Quote: &quote, Ref: &ref}},
		"without a quote": {Title: "Check", WhyOpen: &asked, Source: &openapi.NewTodoSource{Kind: &lark,
			Id: ptr("oc_1"), Ref: &ref}},
		"without a reference": {Title: "Check", WhyOpen: &asked, Source: &openapi.NewTodoSource{Quote: &quote}},
	} {
		r := create(n)
		assert.Equal(t, http.StatusForbidden, r.StatusCode(), "%s: %s", name, r.Body)
	}

	opened := create(openapi.NewTodo{Title: "Check the contract", WhyOpen: &asked,
		Source: &openapi.NewTodoSource{Kind: &lark, Id: ptr("oc_1"), Quote: &quote, Ref: &ref}})
	require.Equal(t, http.StatusCreated, opened.StatusCode(), string(opened.Body))
	assert.Equal(t, openapi.TodoStateOpen, opened.JSON201.State, "why_open opens it")
	assert.Equal(t, &asked, opened.JSON201.WhyOpen)
	assert.Equal(t, openapi.TodoSource{Kind: lark, Id: ptr("oc_1"), Quote: &quote, Ref: &ref}, opened.JSON201.Source)

	got := w.get(t, opened.JSON201.Id)
	require.NotNil(t, got.Events)
	assert.Equal(t, string(asked), (*got.Events)[0].After["why_open"])

	// A Gmail thread and a meeting are named the same way (issue #1105).
	gmail, meeting := openapi.TodoSourceKindGmail, openapi.TodoSourceKindMeeting
	mail := openapi.TodoLink{Kind: openapi.TodoLinkKindMail, Ref: "18c2f0a9b1d2e3f5"}
	line := openapi.TodoLink{Kind: openapi.TodoLinkKindMeetingLine, Ref: "mtg_1/12"}

	for kind, src := range map[openapi.TodoSourceKind]openapi.NewTodoSource{
		gmail:   {Kind: &gmail, Id: ptr("18c2f0a9b1d2e3f4"), Account: ptr("work"), Quote: &quote, Ref: &mail},
		meeting: {Kind: &meeting, Id: ptr("mtg_1"), Quote: &quote, Ref: &line},
	} {
		r := create(openapi.NewTodo{Title: "Check the contract", WhyOpen: &asked, Source: &src})
		require.Equal(t, http.StatusCreated, r.StatusCode(), string(r.Body))
		assert.Equal(t, openapi.TodoStateOpen, r.JSON201.State, kind)
		assert.Equal(t, openapi.TodoSource{Kind: kind, Id: src.Id, Account: src.Account, Quote: &quote,
			Ref: src.Ref}, r.JSON201.Source)
	}

	// A prompt Routine's run, with its own chat.
	prompt, every := "Read my Lark chats.", 60
	made, err := w.owner.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "brief",
		openapi.RoutineSettings{Kind: openapi.Prompt, Prompt: &prompt, EveryMinutes: &every})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode(), string(made.Body))

	started, err := w.svc.RunRoutineNow(ctx, made.JSON200.Id)
	require.NoError(t, err)
	require.NotEmpty(t, started.Run.ChatID)

	pi := w.piToken("pi-coordinator", started.Run.ChatID, orchestration.DefaultBotID)
	r, err := pi.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{Title: "Answer Linh"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, r.StatusCode(), string(r.Body))
	assert.Equal(t, openapi.TodoStateProposed, r.JSON201.State)
	assert.Equal(t, openapi.TodoSource{Kind: openapi.TodoSourceKindRoutine, Id: &started.Run.ID}, r.JSON201.Source)
}

// movedClock is the wall clock moved by an offset.
type movedClock struct{ by atomic.Int64 }

func (c *movedClock) Now() time.Time { return time.Now().Add(time.Duration(c.by.Load())) }

// Scenario: an untouched proposal three days old is stale: the list and the count leave it out, include_stale
// shows it, marked stale; one the owner touched is not stale.
func TestAStaleProposalIsHiddenUntilAskedFor(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clock := &movedClock{}
	w := newInboxWorldAt(t, clock)
	old, touched := w.proposed(t), w.proposed(t)

	clock.by.Store(int64(inboxstore.StaleAfter + time.Minute))

	r, err := w.owner.EditTodoWithResponse(ctx, touched.Id, openapi.TodoEdit{Revision: touched.Revision,
		Notes: ptr("Linh asked again")})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, r.StatusCode(), string(r.Body))

	list := func(p openapi.ListTodosParams) map[string]bool {
		t.Helper()

		r, err := w.owner.ListTodosWithResponse(ctx, &p)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, r.StatusCode(), string(r.Body))
		assert.Len(t, r.JSON200.Data, r.JSON200.Total)

		out := map[string]bool{}
		for _, td := range r.JSON200.Data {
			out[td.Id] = td.Stale
		}

		return out
	}

	assert.Equal(t, map[string]bool{touched.Id: false}, list(openapi.ListTodosParams{}))
	assert.Equal(t, map[string]bool{old.Id: true, touched.Id: false},
		list(openapi.ListTodosParams{IncludeStale: ptr(true)}))
	assert.Equal(t, map[string]bool{touched.Id: false}, list(openapi.ListTodosParams{NeedsYou: ptr(true)}))

	n, err := w.owner.GetInboxNeedsYouWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, n.StatusCode(), string(n.Body))
	assert.Equal(t, 1, n.JSON200.Count)
}

// Scenario: the store's errors as the API answers them: a stale revision is 409 and changes nothing; another
// attempt's id is 409; an invalid request 400; an item that does not exist 404; a create retried with the same key
// answers the first item, and the same key with another request is 409.
func TestInboxErrorsAndRetries(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newInboxWorld(t)
	td := w.add(ctx, t, "Send the contract", toMain)

	r, err := w.owner.EditTodoWithResponse(ctx, td.Id, openapi.TodoEdit{Revision: td.Revision, Notes: ptr("a")})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, r.StatusCode(), string(r.Body))

	stale, err := w.owner.EditTodoWithResponse(ctx, td.Id, openapi.TodoEdit{Revision: td.Revision, Notes: ptr("b")})
	require.NoError(t, err)
	assert.Equal(t, openapi.Conflict, errorCode(t, stale.Body, stale.StatusCode(), http.StatusConflict))
	assert.Equal(t, "a", w.get(t, td.Id).Notes, "a stale write changes nothing")

	started, err := w.chatBot.StartTodoWithResponse(ctx, td.Id, openapi.TodoChange{Revision: r.JSON200.Revision})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, started.StatusCode(), string(started.Body))
	require.NotNil(t, started.JSON200.Work)
	assert.Equal(t, openapi.TodoWorkKindChat, started.JSON200.Work.Kind)
	assert.Equal(t, w.chat, started.JSON200.Work.Id)

	another := "att_other"
	fenced, err := w.chatBot.ReleaseTodoWithResponse(ctx, td.Id, openapi.TodoRelease{
		Revision: started.JSON200.Revision, Attempt: &another, Reason: "Stuck",
	})
	require.NoError(t, err)
	assert.Equal(t, openapi.Conflict, errorCode(t, fenced.Body, fenced.StatusCode(), http.StatusConflict))

	past := time.Now().Add(-time.Hour)
	later := w.add(ctx, t, "Later", toOwner)
	snoozed, err := w.owner.SnoozeTodoWithResponse(ctx, later.Id, openapi.TodoSnooze{Revision: later.Revision,
		Until: &past})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, snoozed.Body, snoozed.StatusCode(), http.StatusBadRequest))

	missing, err := w.owner.GetTodoWithResponse(ctx, "tdo_missing")
	require.NoError(t, err)
	assert.Equal(t, openapi.NotFound, errorCode(t, missing.Body, missing.StatusCode(), http.StatusNotFound))

	key := "brief-2026-11-20-1"
	first, err := w.routineBot.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{IdempotencyKey: &key},
		openapi.NewTodo{Title: "Answer Linh"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, first.StatusCode(), string(first.Body))

	again, err := w.routineBot.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{IdempotencyKey: &key},
		openapi.NewTodo{Title: "Answer Linh"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, again.StatusCode(), string(again.Body))
	assert.Equal(t, first.JSON201.Id, again.JSON201.Id, "a retry answers the first item")
	assert.Equal(t, 1, w.get(t, first.JSON201.Id).Revision)

	other, err := w.routineBot.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{IdempotencyKey: &key},
		openapi.NewTodo{Title: "Answer Minh"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, other.StatusCode(), string(other.Body))
}
