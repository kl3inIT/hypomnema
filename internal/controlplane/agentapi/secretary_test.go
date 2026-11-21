package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
// tracks wrote their lines out of n's order: the store gives them by when they were said.
type laterNotes struct{ lines []meetingstore.Line }

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
	return meetings.Detail{Meeting: named, Lines: m.lines}, nil
}

// Scenario: review of #1037, round 2: the check looked at 09:01, after the meeting was transcribed and before its
// notes were ready; at its next look the meeting is listed, by when its notes became ready. Its transcript is read
// on in the store's order, by when each line was said: every line once, none skipped, whatever their n.
func TestMeetingsAreFoundByTheirNotesAndReadOnInOrder(t *testing.T) {
	t.Parallel()

	lines := make([]meetingstore.Line, 0, 450)
	for i := range 450 {
		// Two tracks: the mic's lines are numbered after the tab's, but said in between.
		n := i/2 + 1
		if i%2 == 1 {
			n = 1000 + i/2
		}
		lines = append(lines, meetingstore.Line{N: n, Start: time.Duration(i) * time.Second, Text: fmt.Sprintf("line %d", i)})
	}

	var agents *agentapi.Server
	w := newWorldWith(t, 0, func(api *agentapi.Server) { agents = api.WithMeetings(laterNotes{lines: lines}) })
	_ = agents
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
	offset := 0

	for range 3 {
		_, text, failed := w.use(pi, agenttool.ToolMeetingGet, fmt.Sprintf(`{"id":"mtg_2","offset":%d}`, offset))
		require.False(t, failed, text)

		var got struct {
			Meeting struct {
				Lines []struct {
					Text string `json:"text"`
				} `json:"lines"`
				Next *int `json:"next_offset"`
			} `json:"meeting"`
		}
		require.NoError(t, json.Unmarshal([]byte(text), &got))

		for _, l := range got.Meeting.Lines {
			seen[l.Text]++
		}

		if got.Meeting.Next == nil {
			break
		}

		offset = *got.Meeting.Next
	}

	assert.Len(t, seen, 450, "every line")
	for line, times := range seen {
		assert.Equal(t, 1, times, line)
	}
}
