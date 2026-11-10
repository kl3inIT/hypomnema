package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
// A prompt that says "crash" makes it exit mid-turn.
func fakePi() int {
	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)

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

func emit(out *json.Encoder, v map[string]any) {
	if err := out.Encode(v); err != nil {
		os.Exit(4)
	}
}

func newSupervisor(t *testing.T) (*orchestration.Service, *Supervisor) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	sup := New(svc, Config{Command: []string{os.Args[0], "fake-pi"}})

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

	chat, err := svc.CreateChat(ctx, "")
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

	chat, err := svc.CreateChat(ctx, "health page")
	require.NoError(t, err)

	run, err := svc.CreateRun(ctx, orchestration.NewRun{Objective: "health", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)

	tasks, err := svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{{Ref: "a", Spec: "a"}})
	require.NoError(t, err)
	require.NoError(t, svc.CancelTask(ctx, tasks[0].ID))

	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator,
		"echo: Message from the Hypomnema control plane: events of your Runs need you.")
}

// Scenario: pi dies mid-turn. The next message starts it again.
func TestPiIsStartedAgainAfterItExits(t *testing.T) {
	t.Parallel()

	svc, _ := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "")
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
		"Message from the Hypomnema control plane: events of your Runs need you.\n",
		"- Task t1 of run r1 is succeeded: hypomnema tasks show t1 --json\n",
		"Act on them, then end your turn; the control plane wakes you again.\n",
		"\nThe owner also wrote:\nalso add docs"), got)
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithCancel(context.WithoutCancel(t.Context()))
}

// pi runs the coordinator profile's model: its provider is an id in pi's
// models.json.
func TestPiRunsTheCoordinatorProfilesModel(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	args, err := sup.modelArgs(ctx)
	require.NoError(t, err)
	assert.Empty(t, args, "the starting set leaves pi's own default")

	_, err = svc.ApplyProvider(ctx, "", "name: ninerouter\nkind: openai_compatible\n"+
		"base_url: http://172.22.0.1:20128/v1\napi_key_ref: file:ninerouter.key\nmodels: [cx/gpt-6.1-sol]\n")
	require.NoError(t, err)

	_, _, err = svc.ApplyProfile(ctx, "", "name: coordinator\ndescription: pi\nruntime: pi\n"+
		"model: {provider: ninerouter, name: cx/gpt-6.1-sol, effort: high}\n")
	require.NoError(t, err)

	args, err = sup.modelArgs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"--provider", "ninerouter", "--model", "cx/gpt-6.1-sol", "--thinking", "high"}, args)
}

// Scenario: the web watches a chat while pi answers: it sees pi start
// working, its thinking and text as written, the tool start and end, and pi
// going idle; of these only the finished messages are kept.
func TestWatchersSeePiWorkLive(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "")
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
