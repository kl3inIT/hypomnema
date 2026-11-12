package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// TestMain lets the test binary play pi: `<test binary> fake-pi <chat>`.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "fake-pi" {
		os.Exit(fakePi())
	}

	os.Exit(m.Run())
}

// fakePi answers like pi in RPC mode: a response per command; for a prompt,
// one turn with a bash call and a reply that echoes the prompt's first line.
// A prompt that says "crash" makes it exit mid-turn; one that says "fail"
// ends in a failed model call; one that says "hang <how>" writes a little and
// waits for an abort, then ends the turn as <how> says (stopped).
func fakePi() int {
	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)

	hanging := ""

	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return 0
		}

		var cmd map[string]any
		if json.Unmarshal(line, &cmd) != nil {
			continue
		}

		reply := map[string]any{"type": "response", "id": cmd["id"], "command": cmd["type"], "success": true}

		if cmd["type"] == "abort" && hanging != "" {
			emit(out, reply)

			for _, rec := range stopped(hanging) {
				emit(out, rec)
			}

			hanging = ""

			continue
		}

		if cmd["type"] != "prompt" {
			emit(out, reply)

			continue
		}

		text, _ := cmd["message"].(string)
		reply["data"] = map[string]any{"disposition": "started"}
		emit(out, reply)

		if strings.Contains(text, "crash") {
			return 3
		}

		if _, how, ok := strings.Cut(text, "hang "); ok {
			hanging, _, _ = strings.Cut(how, "\n")

			emit(out, map[string]any{"type": "agent_start"})
			emit(out, map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}})
			emit(out, map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{
				"type": "text_delta", "contentIndex": 0, "delta": "half an answer",
			}})

			continue
		}

		if strings.Contains(text, "fail") {
			for _, rec := range []map[string]any{
				{"type": "agent_start"},
				{"type": "message_end", "message": map[string]any{
					"role": "assistant", "stopReason": "error", "errorMessage": "overloaded",
				}},
				{"type": "agent_settled", "aborted": false},
			} {
				emit(out, rec)
			}

			continue
		}

		first, _, _ := strings.Cut(text, "\n")
		answer := "echo: " + first
		for _, rec := range turn(answer) {
			emit(out, rec)
		}
	}
}

// turn is one answer as pi writes it: thinking and a tool call, the tool
// running, then the reply.
func turn(reply string) []map[string]any {
	return []map[string]any{
		{"type": "agent_start"},
		{"type": "message_start", "message": map[string]any{"role": "assistant"}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "thinking_delta", "contentIndex": 0, "delta": "check the runs",
		}},
		{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "toolUse",
			"content": []map[string]any{
				{"type": "thinking", "thinking": "check the runs"},
				{"type": "toolCall", "id": "call_1", "name": "bash", "arguments": map[string]any{}},
			},
		}},
		{
			"type": "tool_execution_start", "toolCallId": "call_1", "toolName": "bash",
			"args": map[string]any{"command": "hypomnema runs list --json"},
		},
		{
			"type": "tool_execution_end", "toolCallId": "call_1", "toolName": "bash", "isError": false,
			"durationMs": 12, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "[]"}}},
		},
		{"type": "message_start", "message": map[string]any{"role": "assistant"}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "text_delta", "contentIndex": 0, "delta": reply,
		}},
		{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "stop",
			"content": []map[string]any{{"type": "text", "text": reply}},
		}},
		{"type": "agent_settled", "aborted": false},
	}
}

// stopped is how a turn told to stop ends: an aborted reply with what was
// written, a failed call with nothing, or no reply at all.
func stopped(how string) []map[string]any {
	settled := map[string]any{"type": "agent_settled", "aborted": true}

	switch how {
	case "aborted":
		return []map[string]any{{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "aborted", "errorMessage": "Request was aborted",
			"content": []map[string]any{{"type": "text", "text": "half an answer"}},
		}}, settled}
	case "error":
		return []map[string]any{{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "error", "errorMessage": "Request was aborted",
		}}, settled}
	default:
		return []map[string]any{settled}
	}
}

func emit(out *json.Encoder, v map[string]any) {
	if err := out.Encode(v); err != nil {
		os.Exit(4)
	}
}

func newSupervisor(t *testing.T) (*orchestration.Service, *Supervisor) {
	t.Helper()

	return newSupervisorWith(t, nil)
}

func newSupervisorWith(t *testing.T, keys Keys) (*orchestration.Service, *Supervisor) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	sup := New(svc, Config{Command: []string{os.Args[0], "fake-pi"}, Keys: keys})

	done := make(chan error, 1)
	ctx, cancel := contextWithCancel(t)

	go func() { done <- sup.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	return svc, sup
}

// waitFor polls the chat until a message from role contains want.
func waitFor(t *testing.T, svc *orchestration.Service, chatID string, role orchestration.ChatRole, want string) {
	t.Helper()

	require.Eventually(t, func() bool {
		msgs, err := svc.ListChatMessages(t.Context(), chatID, 0, 0)
		if err != nil {
			return false
		}

		for _, m := range msgs {
			if m.Role == role && strings.Contains(m.Text, want) {
				return true
			}
		}

		return false
	}, 10*time.Second, 20*time.Millisecond, "no %s message containing %q", role, want)
}

// Scenario: the owner writes in a chat; pi starts, answers, and its tool
// call and reply are kept. The message is delivered once.
func TestPiAnswersTheOwner(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "add a health page\nto demo")
	require.NoError(t, err)

	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: add a health page")
	waitFor(t, svc, chat.ID, orchestration.ChatTool, "pi ran bash: hypomnema runs list --json")

	msgs, err := svc.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)

	var thinking, tool []Part

	for _, m := range msgs {
		var parts []Part
		if m.Parts != nil {
			require.NoError(t, json.Unmarshal(m.Parts, &parts))
		}

		switch {
		case m.Role == orchestration.ChatTool:
			tool = parts
		case m.Role == orchestration.ChatCoordinator && m.Text == "":
			thinking = parts
		}
	}

	assert.Equal(t, []Part{{Type: "thinking", Text: "check the runs"}}, thinking,
		"a reply with only thinking and a tool call keeps the thinking")
	require.Len(t, tool, 1)
	assert.Equal(t, "tool_result", tool[0].Type)
	assert.Equal(t, "call_1", tool[0].ID)
	assert.Equal(t, "[]", tool[0].Output)
	assert.JSONEq(t, `{"command":"hypomnema runs list --json"}`, string(tool[0].Args))

	undelivered, err := svc.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, undelivered)

	require.Eventually(t, func() bool { return !sup.Working(chat.ID) }, 5*time.Second, 20*time.Millisecond)
}

// Scenario: pi created a Run from the chat; when its Task is canceled, the
// control plane wakes pi with the event and the command to read it.
func TestPiIsWokenByItsRuns(t *testing.T) {
	t.Parallel()

	svc, _ := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "health page")
	require.NoError(t, err)

	run, err := svc.CreateRun(ctx, orchestration.NewRun{Objective: "health", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)

	tasks, err := svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{{Ref: "a", Spec: "a"}})
	require.NoError(t, err)
	require.NoError(t, svc.CancelTask(ctx, tasks[0].ID))

	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator,
		"echo: Message from the Hypomnema control plane: these need you.")
}

// Scenario: pi dies mid-turn. The next message starts it again.
func TestPiIsStartedAgainAfterItExits(t *testing.T) {
	t.Parallel()

	svc, _ := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "crash please")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		u, err := svc.Undelivered(ctx, chat.ID)

		return err == nil && len(u) == 0
	}, 10*time.Second, 20*time.Millisecond)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "are you back?")
	require.NoError(t, err)

	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: are you back?")
}

func TestPromptJoinsWakeUpsAndOwnerMessages(t *testing.T) {
	t.Parallel()

	got := promptText([]orchestration.ChatMessage{
		{Role: orchestration.ChatSystem, Text: "Task t1 of run r1 is succeeded: hypomnema tasks show t1 --json"},
		{Role: orchestration.ChatOwner, Text: "also add docs"},
	})
	assert.Equal(t, fmt.Sprint(
		"Message from the Hypomnema control plane: these need you.\n",
		"- Task t1 of run r1 is succeeded: hypomnema tasks show t1 --json\n",
		"Act on them, then end your turn; the control plane wakes you again.\n",
		"\nThe owner also wrote:\nalso add docs"), got)
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithCancel(context.WithoutCancel(t.Context()))
}

// pi runs as the chat's Bot, with the Bot's model: its provider is an id in
// pi's models.json. A chat that names no Bot talks with the first one.
func TestPiRunsAsTheChatsBotWithItsModel(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	first, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	args, _, err := sup.botArgs(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"--bot", "coordinator"}, args, "the starting set leaves pi's own default model")

	_, err = svc.ApplyProvider(ctx, "", "name: ninerouter\nkind: openai_compatible\n"+
		"base_url: http://172.22.0.1:20128/v1\napi_key_ref: file:ninerouter.key\nmodels: [cx/gpt-6.1-sol]\n")
	require.NoError(t, err)

	_, err = svc.PutBot(ctx, orchestration.Bot{
		Name: "vmap", Provider: "ninerouter", Model: "cx/gpt-6.1-sol", Effort: "high", Persona: "You answer about VMAP.",
	})
	require.NoError(t, err)

	second, err := svc.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	args, model, err := sup.botArgs(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--bot", "vmap", "--provider", "ninerouter", "--model", "cx/gpt-6.1-sol", "--thinking", "high",
	}, args)
	assert.Equal(t, "cx/gpt-6.1-sol", model, "the chat's gateway key is made for this model")

	args, _, err = sup.botArgs(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"--bot", "coordinator"}, args, "the first chat keeps its Bot")

	// A Lark group's chat is not given the Bot's memory (issue #246); a person's direct chat is.
	_, err = svc.SetLarkChannel(ctx, orchestration.LarkChannel{
		Name: "maps", AppID: "cli_m4p5", Secret: "lark-channel-maps", Bot: "vmap",
		Mode: orchestration.LarkChannelOwner, OwnerOpenID: "ou_owner", Enabled: true,
	})
	require.NoError(t, err)

	group, err := svc.LarkGroupChatFor(ctx, "maps", "oc_group", "Lark: Maps team")
	require.NoError(t, err)

	args, _, err = sup.botArgs(ctx, group.ChatID)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--bot", "vmap", "--group", "--provider", "ninerouter", "--model", "cx/gpt-6.1-sol", "--thinking", "high",
	}, args)

	direct, err := svc.LarkChannelChatFor(ctx, "maps", "oc_dm", "ou_owner", "")
	require.NoError(t, err)

	args, _, err = sup.botArgs(ctx, direct.ChatID)
	require.NoError(t, err)
	assert.NotContains(t, args, "--group")
}

// Scenario: the web watches a chat while pi answers: it sees pi start
// working, its thinking and text as written, the tool start and end, and pi
// going idle; of these only the finished messages are kept.
func TestWatchersSeePiWorkLive(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	frames, stop := sup.Watch(chat.ID)
	defer stop()

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hello")
	require.NoError(t, err)

	var got []string

	timeout := time.After(10 * time.Second)

	for len(got) == 0 || got[len(got)-1] != "state:false" {
		select {
		case f := <-frames:
			data, err := json.Marshal(f.Data)
			require.NoError(t, err)

			var m map[string]any
			_ = json.Unmarshal(data, &m)

			switch f.Event {
			case FrameState:
				got = append(got, fmt.Sprintf("state:%v", m["working"]))
			case FrameDelta:
				got = append(got, fmt.Sprintf("%s:%s", m["kind"], m["delta"]))
			case FrameTool:
				got = append(got, fmt.Sprintf("tool:%s", m["phase"]))
			default:
				got = append(got, f.Event)
			}
		case <-timeout:
			require.FailNow(t, "pi never went idle", "%v", got)
		}
	}

	assert.Equal(t, []string{
		"state:true", "reset", "thinking:check the runs", "tool:start", "tool:end", "reset", "text:echo: hello",
		"state:false",
	}, got)

	require.NoError(t, sup.Abort(ctx, chat.ID), "stopping an idle pi does nothing")
}

func TestToolOutputIsCappedOnACharacter(t *testing.T) {
	t.Parallel()

	out, cut := capText(strings.Repeat("é", 10), 5)
	assert.True(t, cut)
	assert.Equal(t, "éé", out, "never half a character")

	out, cut = capText("short", 5)
	assert.False(t, cut)
	assert.Equal(t, "short", out)
}

// fakeKeys stands for the model gateway: it hands out keys and remembers
// which it was asked to end.
type fakeKeys struct {
	mu      sync.Mutex
	made    int
	models  []string
	revoked map[string]bool
}

func (f *fakeKeys) AddChatKey(_ context.Context, chatID, model string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.made++
	f.models = append(f.models, model)
	id := fmt.Sprintf("gwk_%d", f.made)

	return id, "secret-" + chatID + "-" + id, nil
}

func (f *fakeKeys) RevokeKey(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.revoked == nil {
		f.revoked = map[string]bool{}
	}

	f.revoked[id] = true

	return nil
}

func (f *fakeKeys) ended(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.revoked[id]
}

// Scenario: a chat's pi asks the control plane for its gateway key at each
// model request (issue #204). The key exists for as long as that pi runs: a
// chat with no pi has none, a pi that exits loses its key, and the pi started
// after it gets another.
func TestAChatsKeyLivesAsLongAsItsPi(t *testing.T) {
	t.Parallel()

	keys := &fakeKeys{}
	svc, sup := newSupervisorWith(t, keys)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, ok := sup.Key(chat.ID)
	assert.False(t, ok, "no pi runs for the chat yet")

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hello")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: hello")

	first, ok := sup.Key(chat.ID)
	require.True(t, ok)
	assert.Equal(t, "secret-"+chat.ID+"-gwk_1", first)
	assert.False(t, keys.ended("gwk_1"))

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "crash please")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return keys.ended("gwk_1") }, 10*time.Second, 20*time.Millisecond,
		"the key ends with the pi it was made for")

	_, ok = sup.Key(chat.ID)
	assert.False(t, ok, "a pi that exited has no key to give")

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "are you back?")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: are you back?")

	second, ok := sup.Key(chat.ID)
	require.True(t, ok)
	assert.Equal(t, "secret-"+chat.ID+"-gwk_2", second)
	assert.False(t, keys.ended("gwk_2"))
}

// Scenario: the owner stops pi mid-turn, and pi ends the turn as aborted with
// what it had written, as a failed call, or with no reply. Each way the chat
// keeps what was written and one stopped row, never a failure; a failure
// after that, with no stop asked, is a failure again.
func TestAStoppedTurnEndsInAStoppedRow(t *testing.T) {
	t.Parallel()

	for _, how := range []string{"aborted", "error", "settled"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()

			svc, sup := newSupervisor(t)
			ctx := t.Context()

			chat, err := svc.CreateChat(ctx, "", "")
			require.NoError(t, err)

			_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hang "+how)
			require.NoError(t, err)

			require.Eventually(t, func() bool { return sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)
			require.NoError(t, sup.Abort(ctx, chat.ID))

			waitFor(t, svc, chat.ID, orchestration.ChatSystem, "Stopped")
			require.Eventually(t, func() bool { return !sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)

			_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "fail now")
			require.NoError(t, err)
			waitFor(t, svc, chat.ID, orchestration.ChatSystem, "overloaded")

			msgs, err := svc.ListChatMessages(ctx, chat.ID, 0, 0)
			require.NoError(t, err)

			got := make([]string, 0, len(msgs))
			for _, m := range msgs {
				got = append(got, fmt.Sprintf("%s/%s: %s", m.Role, m.Kind, m.Text))
			}

			want := []string{"owner/: hang " + how}
			if how == "aborted" {
				want = append(want, "coordinator/: half an answer")
			}

			want = append(want, "system/stopped: Stopped", "owner/: fail now",
				"system/failed: pi's model call failed: overloaded")
			assert.Equal(t, want, got)
		})
	}
}
