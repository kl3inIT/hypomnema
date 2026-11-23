package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// recordedMeetings stands for the meetings the owner recorded: one, its notes ready.
type recordedMeetings struct{}

var standup = meetingstore.Meeting{
	ID: "mtg_1", Title: "VMAP standup", State: meetingstore.Transcribed, CreatedAt: time.Unix(1_800_000_000, 0),
	UpdatedAt: time.Unix(1_800_000_600, 0), Notes: meetingstore.Notes{State: meetingstore.NotesReady,
		Summary: "The map tiles ship Friday."},
	Speakers: []meetingstore.Speaker{{Track: meetingstore.TrackMic, Label: "A", Owner: true}},
	Actions:  1, OpenActions: 1,
}

func (recordedMeetings) List(context.Context, string, int) ([]meetingstore.Meeting, error) {
	return []meetingstore.Meeting{standup}, nil
}

func (recordedMeetings) Get(_ context.Context, id string) (meetings.Detail, error) {
	if id != standup.ID {
		return meetings.Detail{}, orchestration.ErrNotFound
	}

	return meetings.Detail{
		Meeting: standup,
		Lines:   []meetingstore.Line{{N: 1, Track: meetingstore.TrackMic, Speaker: "You", Text: "Để em check tiles."}},
		Items: []meetingstore.Item{{ID: "itm_1", Kind: meetingstore.Action, Text: "Check the tiles",
			OwnerTrack: meetingstore.TrackMic, OwnerSpeaker: "A", Due: "Friday", Quote: "Để em check tiles.", Line: 1}},
	}, nil
}

// secretaryWorld is a control plane whose owner has Lark, Google and Zalo accounts, a Bot "Thư ký" that may read
// them and keeps his Inbox, and its read Routine with a check script; lark answers each Lark read with its status.
type secretaryWorld struct {
	*world

	bot     orchestration.Bot
	routine routinestore.Routine
	lark    atomic.Int32
	// larkBody is what each Lark read answers, with lark's status.
	larkBody atomic.Pointer[string]
	sent     atomic.Int32
}

func newSecretaryWorld(t *testing.T) *secretaryWorld {
	t.Helper()

	ctx := t.Context()
	sw := &secretaryWorld{}
	sw.lark.Store(http.StatusOK)
	sw.larkBody.Store(new(`{"code":0,"data":{"items":[]}}`))

	var agents *agentapi.Server
	sw.world = newWorldWith(t, 0, func(api *agentapi.Server) { agents = api.WithMeetings(recordedMeetings{}) })
	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: sw.svc, Logger: slog.New(slog.DiscardHandler),
		Token: func(_ context.Context, integration, _ string) (string, error) {
			return integration + "-owner-token", nil
		},
		Upstream: accountTransport(func(req *http.Request) (*http.Response, error) {
			status, body := http.StatusOK, `{"items":[]}`
			switch {
			case req.URL.Host == "open.larksuite.com":
				status, body = int(sw.lark.Load()), *sw.larkBody.Load()
			case req.Method == http.MethodPost:
				sw.sent.Add(1)
			}

			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	require.NoError(t, err)
	agents.WithRelay(proxy)

	for _, integration := range []string{"lark", "google", "zalo"} {
		conn := orchestration.Connection{Integration: integration, Label: "me", Secret: "test-" + integration}
		if integration == "lark" {
			conn.AppLabel = "test-app"
		}
		_, err := sw.svc.SaveServiceConnection(ctx, conn)
		require.NoError(t, err)
		_, err = sw.svc.SetConnectionChats(ctx, integration, "me", true)
		require.NoError(t, err)
	}

	sw.bot, err = sw.svc.PutBot(ctx, orchestration.Bot{
		Name: "Thư ký", Connections: orchestration.AllowList{
			Limited: true, Names: []string{"lark:me", "google:me", "zalo:me"},
		},
	})
	require.NoError(t, err)
	_, _, err = sw.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	sw.routine, err = sw.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: sw.bot.ID, Name: "triage", Kind: routinestore.RoutinePrompt, Prompt: "Triage the new messages.",
		Script: "new-messages.mjs", ScriptText: "#!/usr/bin/env node\n", EveryMinutes: 15,
		Access: routinestore.AccessRead,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	return sw
}

// check starts a run of the Routine and gives its check script's token, as the runner makes it for the script.
func (sw *secretaryWorld) check() (token, runID string) {
	sw.t.Helper()

	run, err := sw.svc.RunRoutineNow(sw.t.Context(), sw.routine.ID)
	require.NoError(sw.t, err)
	require.Empty(sw.t, run.Run.ChatID, "a check runs before any chat")

	token, err = sw.svc.AddRoutineToken(sw.t.Context(), "pi", sw.routine.ID)
	require.NoError(sw.t, err)

	return token, run.Run.ID
}

// end ends the run of the Routine that is live, as the runner does when its check woke no one.
func (sw *secretaryWorld) end(runID string) {
	sw.t.Helper()

	_, err := sw.svc.FinishRoutineRun(sw.t.Context(), runID,
		routinestore.RunResult{State: routinestore.RunSucceeded})
	require.NoError(sw.t, err)
}

// Scenario: issue #1023, section 2, with the owner's decision of 2026-11-21: the read Routine of the Thư ký Bot
// has a check script that counts the new messages before any model is woken. The script reads with the rights of
// the Routine's read run, through the same token any Routine's script has: the owner's Zalo, Lark and Gmail reads
// and his meetings. Nothing else: no send as him on Zalo or WhatsApp, no Gmail send, no Inbox write, no shell on a
// node; a refused call answers that the run reads only. A Routine that is not read gives its script no account of
// the owner's at all.
func TestAReadRoutinesCheckReadsAsItsRunAndNothingMore(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	script, _ := sw.check()

	listed := toolNames(sw.tools(script))
	for _, tool := range []string{agenttool.ToolAccountRead, agenttool.ToolZaloChats, agenttool.ToolZaloMessages,
		agenttool.ToolMeetingList, agenttool.ToolMeetingGet} {
		assert.Contains(t, listed, tool)
	}

	_, text, failed := sw.use(script, agenttool.ToolZaloMessages, `{"chat":"g_pmo"}`)
	require.False(t, failed, text)
	assert.Equal(t, []string{"zalo:me:g_pmo"}, sw.whatsapp.asked)

	// Every chat's messages, a page on: the check pages by offset, which never skips messages sent at the same
	// time (review of #1037, round 2).
	_, text, failed = sw.use(script, agenttool.ToolZaloMessages, `{"limit":500,"offset":500}`)
	require.False(t, failed, text)
	assert.Equal(t, 500, sw.whatsapp.queries[len(sw.whatsapp.queries)-1].Offset)

	_, text, failed = sw.use(script, agenttool.ToolAccountRead, `{"account":"google:me",
		"host":"gmail.googleapis.com","method":"GET","path":"/gmail/v1/users/me/messages?q=newer_than:1d"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, "untrusted data")

	_, text, failed = sw.use(script, agenttool.ToolMeetingList, `{"since":"2028-01-01T00:00:00Z"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, `"meetings": []`, "none since then")

	_, text, failed = sw.use(script, agenttool.ToolAccountRead, `{"account":"google:me",
		"host":"gmail.googleapis.com","method":"POST","path":"/gmail/v1/users/me/messages/send","body":{"raw":"x"}}`)
	assert.True(t, failed, "a Gmail send: %s", text)
	assert.Contains(t, text, agenttool.ReadRunRefusal)
	assert.Zero(t, sw.sent.Load(), "nothing was sent to Google")

	for _, tool := range []string{agenttool.ToolZaloSend, agenttool.ToolWhatsAppSend, "node_exec",
		agenttool.ToolWebFetch, agenttool.ToolShowArtifact} {
		assert.NotContains(t, listed, tool)

		_, text, failed := sw.use(script, tool, `{"chat":"g_pmo","text":"Dạ em gửi anh báo giá ạ"}`)
		assert.True(t, failed, tool)
		assert.Contains(t, text, agenttool.ReadRunRefusal, tool)
	}

	// The Inbox and the memory are the run's chat's: a check has neither.
	for _, tool := range []string{agenttool.ToolInboxAdd, agenttool.ToolMemory} {
		assert.NotContains(t, listed, tool)

		status, text, _ := sw.use(script, tool, `{"title":"Reply to Lan"}`)
		assert.Equal(t, http.StatusNotFound, status, "%s: %s", tool, text)
	}

	assert.Empty(t, sw.whatsapp.sent, "nothing is sent as the owner")
	assert.Empty(t, sw.judge.asked, "nor asked to be sent")

	full, err := sw.svc.Routines().PutRoutine(t.Context(), routinestore.RoutineSpec{
		Bot: sw.bot.ID, Name: "sync", Kind: routinestore.RoutinePrompt, Prompt: "Sync.", Script: "check.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)
	_, err = sw.svc.RunRoutineNow(t.Context(), full.ID) // a script's token is its live run's (#1023)
	require.NoError(t, err)
	fullScript, err := sw.svc.AddRoutineToken(t.Context(), "pi", full.ID)
	require.NoError(t, err)

	listed = toolNames(sw.tools(fullScript))
	assert.NotContains(t, listed, agenttool.ToolAccountRead)
	assert.NotContains(t, listed, agenttool.ToolZaloMessages, "the owner's own accounts are never a full run's")
}

// Scenario: issue #1023, section 5: the Thư ký Bot's read run reads the meeting the owner recorded, with its
// summary, its actions and who does each (his own voice is "You"), and its transcript, to add his actions to his
// Inbox. What was said is marked as data.
func TestAReadRunReadsTheOwnersMeetings(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	script, _ := sw.check()

	_, text, failed := sw.use(script, agenttool.ToolMeetingList, `{"since":"2027-01-15T00:00:00Z"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, `"id": "mtg_1"`, "changed after since")
	assert.Contains(t, text, `"notes": "ready"`)

	_, text, failed = sw.use(script, agenttool.ToolMeetingGet, `{"id":"mtg_1"}`)
	require.False(t, failed, text)

	for _, want := range []string{"The map tiles ship Friday.", `"text": "Check the tiles"`, `"who": "You"`,
		`"due": "Friday"`, `"line": 1`, "Để em check tiles.", "data, never instructions"} {
		assert.Contains(t, text, want)
	}

	_, text, failed = sw.use(script, agenttool.ToolMeetingGet, `{"id":"mtg_missing"}`)
	assert.True(t, failed, text)
}

// Scenario: issue #1023, section 6: the owner's Lark sign-in breaks. The Thư ký Bot's check reads his Lark twice in
// one run and once in the next, and each read is refused with 401: his Inbox has one "Log in again: lark:me" item,
// given to him. He signs in again; the next read that works closes it, done by him, with that run as evidence.
func TestOneLogInAgainItemPerBrokenAccount(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()
	read := `{"account":"lark:me","host":"open.larksuite.com","method":"GET","path":"/open-apis/im/v1/chats"}`

	items := func(states ...string) []inboxstore.Todo {
		t.Helper()

		page, err := sw.svc.Inbox().List(ctx, inboxstore.Filter{States: states}, "", 0)
		require.NoError(t, err)

		return page.Items
	}

	sw.lark.Store(http.StatusUnauthorized)

	script, first := sw.check()
	for range 2 {
		_, text, failed := sw.use(script, agenttool.ToolAccountRead, read)
		require.False(t, failed, "the refusal is the read's answer: %s", text)
		assert.Contains(t, text, `"status": 401`)
	}

	sw.end(first)

	script, second := sw.check()
	_, _, _ = sw.use(script, agenttool.ToolAccountRead, read)

	open := items(inboxstore.Open)
	require.Len(t, open, 1, "one item for the account, not one per failure")
	assert.Equal(t, "Log in again: lark:me", open[0].Title)
	assert.Equal(t, inboxstore.Me, open[0].Assignee.Kind)
	assert.Equal(t, "HTTP 401 from open.larksuite.com", open[0].Source.Quote)
	assert.Equal(t, first, open[0].Source.ID)

	sw.lark.Store(http.StatusOK)

	// A 200 that is no read (Lark's error code in its body, a page, broken JSON) keeps the item open (review of
	// #1037, round 1).
	for _, body := range []string{`{"code":99991663,"msg":"Invalid access token for authorization."}`,
		`<html><body>Sign in</body></html>`, `{"code":0,"data":`} {
		sw.larkBody.Store(&body)
		_, _, _ = sw.use(script, agenttool.ToolAccountRead, read)

		still := items(inboxstore.Open)
		require.Len(t, still, 1, "a 200 answering %s is no read that works", body)
		assert.Equal(t, open[0].ID, still[0].ID)
	}

	sw.larkBody.Store(new(`{"code":0,"data":{"items":[]}}`))

	_, text, failed := sw.use(script, agenttool.ToolAccountRead, read)
	require.False(t, failed, text)

	assert.Empty(t, items(inboxstore.Open))

	done := items(inboxstore.Done)
	require.Len(t, done, 1)
	assert.Equal(t, open[0].ID, done[0].ID)
	assert.Equal(t, inboxstore.Me, done[0].DoneBy.Kind)
	assert.Equal(t, []inboxstore.Link{{Kind: inboxstore.LinkRun, Ref: second, Label: "lark:me was read"}},
		done[0].Evidence)
}

// Scenario: review of #1037, round 2: the owner switches the Thư ký's triage Routine to full while its check runs,
// and a Bot's edit of it takes the read grant back: the check of the run that goes on keeps reading only (no send
// as him, no Inbox), as its run started. A run of a full Routine that the owner marks read meanwhile is never
// given his own accounts.
func TestARunKeepsTheAccessItStartedWith(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()
	script, _ := sw.check()

	_, err := sw.svc.Routines().SetRoutineAccess(ctx, sw.routine.ID, routinestore.AccessFull)
	require.NoError(t, err)

	listed := toolNames(sw.tools(script))
	assert.Contains(t, listed, agenttool.ToolAccountRead, "the run reads as it started")
	assert.NotContains(t, listed, agenttool.ToolZaloSend)

	_, text, failed := sw.use(script, agenttool.ToolZaloSend, `{"chat":"g_pmo","text":"Dạ em gửi anh báo giá ạ"}`)
	assert.True(t, failed)
	assert.Contains(t, text, agenttool.ReadRunRefusal)
	assert.Empty(t, sw.whatsapp.sent)

	full, err := sw.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: sw.bot.ID, Name: "sync", Kind: routinestore.RoutinePrompt, Prompt: "Sync.", Script: "check.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)
	_, err = sw.svc.RunRoutineNow(ctx, full.ID)
	require.NoError(t, err)
	fullScript, err := sw.svc.AddRoutineToken(ctx, "pi", full.ID)
	require.NoError(t, err)

	_, err = sw.svc.Routines().SetRoutineAccess(ctx, full.ID, routinestore.AccessRead)
	require.NoError(t, err)

	listed = toolNames(sw.tools(fullScript))
	assert.NotContains(t, listed, agenttool.ToolAccountRead, "a run that started full never reads his accounts")
	assert.NotContains(t, listed, agenttool.ToolZaloMessages)
}

// laterNotes stands for a meeting named and transcribed at 09:00 whose notes became ready at 10:00, and whose two
// tracks wrote their lines out of n's order: the store gives them by when they were said. lines may grow, as a
// live meeting's do.
type laterNotes struct{ lines *[]meetingstore.Line }

var named = meetingstore.Meeting{
	ID: "mtg_2", Title: "Kai sync", Titled: true, State: meetingstore.Transcribed,
	CreatedAt: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC),
	Notes: meetingstore.Notes{State: meetingstore.NotesReady,
		WrittenAt: new(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))},
	Actions: 1, OpenActions: 1,
}

func (laterNotes) List(context.Context, string, int) ([]meetingstore.Meeting, error) {
	return []meetingstore.Meeting{named}, nil
}

func (m laterNotes) Get(context.Context, string) (meetings.Detail, error) {
	return meetings.Detail{Meeting: named, Lines: slices.Clone(*m.lines)}, nil
}

// Scenario: review of #1037, rounds 2 and 3: the check looked at 09:01, after the meeting was transcribed and
// before its notes were ready; at its next look the meeting is listed, by when its notes became ready. Its
// transcript is read on by each line's number, which only grows: every line once, none skipped, whatever their
// order in time, even when a line said early arrives between two pages, as a live meeting's track appends it.
func TestMeetingsAreFoundByTheirNotesAndReadOnInOrder(t *testing.T) {
	t.Parallel()

	lines := make([]meetingstore.Line, 0, 451)
	for i := range 450 {
		// Two tracks: the mic's lines are numbered after the tab's, but said in between.
		n := i/2 + 1
		if i%2 == 1 {
			n = 1000 + i/2
		}
		lines = append(lines, meetingstore.Line{N: n, Start: time.Duration(i) * time.Second, Text: fmt.Sprintf("line %d", i)})
	}

	w := newWorldWith(t, 0, func(api *agentapi.Server) { api.WithMeetings(laterNotes{lines: &lines}) })
	ctx := t.Context()
	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "Thư ký"})
	require.NoError(t, err)
	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	_, text, failed := w.use(pi, agenttool.ToolMeetingList, `{"since":"2026-11-21T09:01:00Z"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, `"id": "mtg_2"`, "its notes became ready after since")

	seen := map[string]int{}
	after := 0

	for page := range 4 {
		_, text, failed := w.use(pi, agenttool.ToolMeetingGet, fmt.Sprintf(`{"id":"mtg_2","after_line":%d}`, after))
		require.False(t, failed, text)

		var got struct {
			Meeting struct {
				Lines []struct {
					Text string `json:"text"`
				} `json:"lines"`
				Next *int `json:"next_after_line"`
			} `json:"meeting"`
		}
		require.NoError(t, json.Unmarshal([]byte(text), &got))

		for _, l := range got.Meeting.Lines {
			seen[l.Text]++
		}

		if page == 0 {
			// Between two pages, the mic's track appends a line said at 00:05, before every line read on.
			late := meetingstore.Line{N: 2000, Start: 5 * time.Second, Text: "a late line"}
			i := slices.IndexFunc(lines, func(l meetingstore.Line) bool { return l.Start > late.Start })
			lines = slices.Insert(lines, i, late)
		}

		if got.Meeting.Next == nil {
			break
		}

		after = *got.Meeting.Next
	}

	assert.Len(t, seen, 451, "every line, the late one too")
	for line, times := range seen {
		assert.Equal(t, 1, times, line)
	}
}

// Scenario: issue #1023 with the owner's choice of 2026-11-21 (review of #1037, round 2): in the Thư ký's own chat,
// on the owner's turn, it reads his Zalo but has no send as him: zalo_send is neither listed nor run, whatever he
// approved, and nothing is asked.
func TestABotThatReadsTheOwnersAccountsHasNoSend(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()
	bot := sw.bot
	bot.OwnerAccounts = orchestration.OwnerAccountsRead
	_, err := sw.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	chat, err := sw.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	_, err = sw.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "nhắn Lan giúp anh là mai em gửi")
	require.NoError(t, err)
	pi, err := sw.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	listed := toolNames(sw.tools(pi))
	assert.Contains(t, listed, agenttool.ToolZaloMessages, "reads stay")
	assert.NotContains(t, listed, agenttool.ToolZaloSend)

	_, text, failed := sw.use(pi, agenttool.ToolZaloSend, `{"chat":"g_lan","text":"Mai em gửi ạ"}`)
	assert.True(t, failed)
	assert.Contains(t, text, agenttool.OwnerReadOnlyRefusal)
	assert.Empty(t, sw.whatsapp.sent, "nothing is sent as the owner")
	assert.Empty(t, sw.judge.asked, "nor asked")
}

// Scenario: issue #1105, section 1, the owner's ask of 2026-11-23. In its own chat with the owner, not a Routine's
// run, the Thư ký reads his Zalo, his Gmail and a meeting and puts what it found in his Inbox from where it read it:
// his promise to cô Laura on Zalo opens with why_open, a mail with nothing clear in it is proposed, and the meeting's
// action points at its line. An account the Bot may not use is refused, as its reads are, and nothing is added. An
// item it added from the chat is pointed at the Zalo message it came from with inbox_update.
func TestABotAddsFromWhatItReadInItsChat(t *testing.T) {
	t.Parallel()

	sw := newSecretaryWorld(t)
	ctx := t.Context()

	_, err := sw.svc.SaveServiceConnection(ctx, orchestration.Connection{Integration: "zalo", Label: "work",
		Secret: "test-zalo-work"})
	require.NoError(t, err)
	_, err = sw.svc.SetConnectionChats(ctx, "zalo", "work", true)
	require.NoError(t, err)

	pi, _ := sw.inboxPi(sw.bot)
	promise := map[string]any{
		"title":  "Gửi cô Laura báo cáo VETC, Goong, Cốc Cốc",
		"source": map[string]any{"kind": inboxstore.FromZalo, "chat": "g_laura", "account": "me"},
		"quote":  "tầm chiều tối em gửi cô cái báo cáo các app vetc,... nhé ạ", "why_open": inboxstore.Promised,
		"message": map[string]any{"kind": inboxstore.LinkZaloMessage, "ref": "z_20"},
	}

	laura := sw.inbox(pi, agenttool.ToolInboxAdd, promise)
	assert.Equal(t, inboxstore.Open, laura.State)
	assert.Equal(t, inboxstore.Promised, laura.WhyOpen)
	assert.Equal(t, inboxstore.FromZalo, laura.Source.Kind)
	assert.Equal(t, "g_laura", laura.Source.ID)
	assert.Equal(t, "me", laura.Source.Account)

	mail := sw.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title":  "Xem đề xuất PoC của Alibaba Cloud",
		"source": map[string]any{"kind": inboxstore.FromGmail, "thread": "18c2f0a9b1d2e3f4", "account": "me"},
		"quote":  "Please find our PoC proposal attached.",
		"message": map[string]any{"kind": inboxstore.LinkMail, "ref": "18c2f0a9b1d2e3f5",
			"label": "Alibaba Cloud"},
	})
	assert.Equal(t, inboxstore.Proposed, mail.State, "nothing asked him directly: proposed, as in a run")
	assert.Equal(t, inboxstore.FromGmail, mail.Source.Kind)
	assert.Equal(t, "18c2f0a9b1d2e3f4", mail.Source.ID)
	assert.Equal(t, "me", mail.Source.Account)
	assert.Equal(t, &inboxstore.Link{Kind: inboxstore.LinkMail, Ref: "18c2f0a9b1d2e3f5", Label: "Alibaba Cloud"},
		mail.Source.Message)

	tiles := sw.inbox(pi, agenttool.ToolInboxAdd, map[string]any{
		"title":  "Check tiles",
		"source": map[string]any{"kind": inboxstore.FromMeeting, "meeting": standup.ID},
		"quote":  "Để em check tiles.", "why_open": inboxstore.Promised,
		"message": map[string]any{"kind": inboxstore.LinkMeetingLine, "ref": standup.ID + "/1"},
	})
	assert.Equal(t, inboxstore.Open, tiles.State)
	assert.Equal(t, inboxstore.FromMeeting, tiles.Source.Kind)
	assert.Equal(t, standup.ID, tiles.Source.ID)
	assert.Empty(t, tiles.Source.Account)

	for kind, want := range map[string]string{inboxstore.FromGmail: mail.ID, inboxstore.FromMeeting: tiles.ID} {
		assert.Equal(t, []string{want}, sw.inboxList(pi, map[string]any{"source_kind": kind,
			"state": []string{inboxstore.Open, inboxstore.Proposed}}), kind)
	}

	for name, src := range map[string]map[string]any{
		"an account it may not use": {"kind": inboxstore.FromZalo, "chat": "g_laura", "account": "work"},
		"an account that is not":    {"kind": inboxstore.FromGmail, "thread": "t_1", "account": "nobody"},
		"no account":                {"kind": inboxstore.FromZalo, "chat": "g_laura"},
		"a meeting that is not":     {"kind": inboxstore.FromMeeting, "meeting": "mtg_404"},
	} {
		refused := maps.Clone(promise)
		refused["source"], refused["key"] = src, name

		_, text, failed := sw.use(pi, agenttool.ToolInboxAdd, mustJSON(t, refused))
		assert.True(t, failed, "%s: %s", name, text)
	}

	all, err := sw.svc.Inbox().List(ctx, inboxstore.Filter{WithClosed: true}, "", 0)
	require.NoError(t, err)
	assert.Equal(t, 3, all.Total, "nothing refused was added")

	added := sw.inbox(pi, agenttool.ToolInboxAdd, map[string]any{"title": "Gửi cô Laura báo cáo"})
	assert.Equal(t, inboxstore.FromChat, added.Source.Kind)

	moved := sw.inbox(pi, agenttool.ToolInboxUpdate, map[string]any{
		"id": added.ID, "source": promise["source"], "quote": promise["quote"], "message": promise["message"],
	})
	assert.Equal(t, inboxstore.FromZalo, moved.Source.Kind)
	assert.Equal(t, "g_laura", moved.Source.ID)
	assert.Equal(t, inboxstore.Open, moved.State)

	_, text, failed := sw.use(pi, agenttool.ToolInboxUpdate, mustJSON(t, map[string]any{"id": added.ID,
		"source":  map[string]any{"kind": inboxstore.FromZalo, "chat": "g_laura", "account": "work"},
		"message": promise["message"]}))
	assert.True(t, failed, "repointed at an account it may not use: %s", text)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	return string(raw)
}
