package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
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
// A prompt that says "crash" makes it exit mid-turn, and one that says "die
// mid-turn" exits after its turn started; one that says "fail" ends in a
// failed model call; one that says "hang <how>" writes a little and
// waits for an abort, then ends the turn as <how> says (stopped). A message
// steered meanwhile is answered queued and held: clear_queue takes it out
// while the turn runs, too late after the abort (pi before 0.86 starts it as
// the abort lands, t3code adapter.ts clearNativeQueue), and one the abort
// left is given with the next prompt's answer, as pi keeps it queued for the
// next run; a steer that says "finish" has pi read every steer it holds, a
// user message each, and end the turn with them; one that says "exit now"
// makes it exit before it read them. One that
// asks for the "token" is answered with the line before the commands, which
// the launcher reads; one that asks "which model" is answered with the model
// and thinking level of its flags, or of the set_model and
// set_thinking_level it was sent since. Once a prompt said "context", it
// reports its context usage (get_session_stats) at 1000 tokens an answer, and
// none right after a compaction; a compact after a prompt that said "too
// small" fails as pi's does on a short session. A prompt that says "overflow" stays too large after pi compacted
// and tried again, and ends as pi 1.5.2 ends it.
func fakePi() int {
	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	token := ""
	model, level := flagOf("--provider")+"/"+flagOf("--model"), flagOf("--thinking")

	hanging, steered := "", []string(nil)
	measured, small, compacted, answers := false, false, false, 0

	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return 0
		}

		var cmd map[string]any
		if json.Unmarshal(line, &cmd) != nil {
			if token == "" {
				token = strings.TrimSpace(string(line))
			}

			continue
		}

		reply := map[string]any{"type": "response", "id": cmd["id"], "command": cmd["type"], "success": true}

		if cmd["type"] == "abort" && hanging != "" {
			emit(out, reply)

			for _, rec := range stopped(hanging) {
				emit(out, rec)
			}

			// What was steered in meanwhile stays in pi's queue: an abort ends the run without going on to it, and
			// the next run takes it in at its start (pi agent-session.ts _startRun, agent-loop.ts runLoop; #575).
			hanging = ""

			continue
		}

		if cmd["type"] == "prompt" && cmd["streamingBehavior"] == "steer" && hanging != "" {
			text := fmt.Sprint(cmd["message"])
			first, _, _ := strings.Cut(text, "\n")
			steered = append(steered, first)
			reply["data"] = map[string]any{"disposition": "queued"}
			emit(out, reply)

			if strings.Contains(text, "exit now") {
				return 3
			}

			if strings.Contains(text, "finish") {
				for _, m := range steered {
					user := map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": m}}}
					emit(out, map[string]any{"type": "message_start", "message": user})
					emit(out, map[string]any{"type": "message_end", "message": user})
				}

				// The turn goes on: it started once.
				for _, rec := range turn("read: " + strings.Join(steered, " | "))[1:] {
					emit(out, rec)
				}

				hanging, steered = "", nil
			}

			continue
		}

		switch cmd["type"] {
		case "clear_queue":
			cleared := []string{}
			if hanging != "" {
				cleared, steered = append(cleared, steered...), nil
			}

			reply["data"] = map[string]any{"steering": cleared, "followUp": []string{}}
		case "set_model":
			model = fmt.Sprint(cmd["provider"], "/", cmd["modelId"])
		case "set_thinking_level":
			level = fmt.Sprint(cmd["level"])
		case "get_session_stats":
			reply["data"] = sessionStatsOf(measured, compacted, answers)
		case "compact":
			emit(out, map[string]any{"type": "compaction_start", "reason": "manual"})

			if small {
				failure := "Nothing to compact (session too small)"
				emit(out, map[string]any{"type": "compaction_end", "reason": "manual", "errorMessage": failure})
				emit(out, map[string]any{
					"type": "response", "id": cmd["id"], "command": "compact", "success": false, "error": failure,
				})

				continue
			}

			compacted = true
			reply["data"] = map[string]any{"summary": "the gist", "tokensBefore": answers * 1000}
			emit(out, map[string]any{"type": "compaction_end", "reason": "manual", "result": reply["data"]})
		}

		if cmd["type"] != "prompt" {
			emit(out, reply)

			continue
		}

		text, _ := cmd["message"].(string)
		reply["data"] = map[string]any{"disposition": "started"}
		emit(out, reply)

		measured = measured || strings.Contains(text, "context")
		small = strings.Contains(text, "too small")

		if strings.Contains(text, "crash") {
			return 3
		}

		if strings.Contains(text, "die mid-turn") {
			emit(out, map[string]any{"type": "agent_start"})

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

		if strings.Contains(text, "overflow") {
			for _, rec := range overflowed() {
				emit(out, rec)
			}

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

		if len(steered) > 0 {
			answer += " (steered: " + strings.Join(steered, " | ") + ")"
			steered = nil
		}

		if strings.Contains(text, "token") {
			answer = "my token: " + token
		}

		if strings.Contains(text, "which model") {
			answer = "model " + model + " thinking " + level
		}

		answers++
		compacted = false

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
		{"type": "turn_end"},
		{"type": "agent_settled", "aborted": false},
	}
}

// tooLarge is the gateway's refusal of a request over its limit, as pi reports it.
const tooLarge = `413 {"error":{"message":"the request is over 33554432 bytes","type":"invalid_request_error"},` +
	`"type":"error"}`

// recoveryFailed is what pi says when a request still overflows after it compacted and tried again.
const recoveryFailed = "Context overflow recovery failed after one compact-and-retry attempt. " +
	"Try reducing context or switching to a larger-context model."

// overflowed is a turn whose request is too large, before and after pi compacts: pi compacts and tries again once,
// then ends the compaction with its errorMessage and settles (pi agent-session.ts, _checkCompaction).
func overflowed() []map[string]any {
	failed := map[string]any{"type": "message_end", "message": map[string]any{
		"role": "assistant", "stopReason": "error", "errorMessage": tooLarge,
	}}

	return []map[string]any{
		{"type": "agent_start"},
		failed,
		{"type": "compaction_start", "reason": "overflow"},
		{
			"type": "compaction_end", "reason": "overflow", "aborted": false, "willRetry": true,
			"result": map[string]any{"summary": "the gist", "tokensBefore": 900000},
		},
		failed,
		{
			"type": "compaction_end", "reason": "overflow", "aborted": false, "willRetry": false,
			"errorMessage": recoveryFailed,
		},
		{"type": "agent_settled", "aborted": false},
	}
}

// sessionStatsOf is the fake pi's get_session_stats: no contextUsage until it
// is measured, and no tokens right after a compaction (pi rpc-commands.md).
func sessionStatsOf(measured, compacted bool, answers int) map[string]any {
	if !measured {
		return map[string]any{}
	}

	usage := map[string]any{"tokens": nil, "contextWindow": 128000, "percent": nil}
	if !compacted {
		usage["tokens"], usage["percent"] = answers*1000, float64(answers*1000)/1280
	}

	return map[string]any{"contextUsage": usage}
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

// flagOf is the value of one of the flags the fake pi was started with, or "".
func flagOf(name string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}

	return ""
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

	return newSupervisorConfig(t, Config{Keys: keys})
}

// newSupervisorConfig runs a supervisor of fake pi processes with cfg.
func newSupervisorConfig(t *testing.T, cfg Config) (*orchestration.Service, *Supervisor) {
	t.Helper()

	svc, _ := newStore(t)
	sup, _ := runSupervisor(t, svc, cfg)

	return svc, sup
}

// newStore is the service on a migrated database, for a test to prepare before a supervisor runs, and its clock,
// the system's until the test sets it.
func newStore(t *testing.T) (*orchestration.Service, *testClock) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	clock := &testClock{}

	return orchestration.New(db, clock, platform.NewID), clock
}

// testClock is the system's clock, or the time a test set.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.now.IsZero() {
		return time.Now()
	}

	return c.now
}

func (c *testClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = now
}

// runSupervisor runs a supervisor of fake pi processes on svc until the test ends, or until the returned stop is
// called, which waits for it to return.
func runSupervisor(t *testing.T, svc *orchestration.Service, cfg Config) (*Supervisor, func()) {
	t.Helper()

	cfg.Command = []string{os.Args[0], "fake-pi"}
	sup := New(svc, cfg)

	done := make(chan error, 1)
	ctx, cancel := contextWithCancel(t)

	go func() { done <- sup.Run(ctx) }()

	var once sync.Once

	stop := func() {
		once.Do(func() {
			cancel()

			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}

	t.Cleanup(stop)

	return sup, stop
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

// Scenario: a chat has a Run and nothing happens (issue #586). The coordinator
// writes nothing, so no commit wakes it or anyone else; it once woke itself
// with an empty commit per pass and read the database in a loop. A Task that
// ends still wakes pi.
func TestAnIdleCoordinatorDoesNotWakeItself(t *testing.T) {
	t.Parallel()

	svc, _ := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "health page")
	require.NoError(t, err)

	run, err := svc.CreateRun(ctx, orchestration.NewRun{Objective: "health", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)

	tasks, err := svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{{Ref: "a", Spec: "a"}})
	require.NoError(t, err)

	commits := svc.Subscribe(ctx)
	window := time.After(time.Second)
	woken := 0

count:
	for {
		select {
		case <-commits:
			woken++
		case <-window:
			break count
		}
	}

	assert.Zero(t, woken, "an idle coordinator commits nothing")

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

// A turn is of one kind (issue #1172): the owner's messages go as they were written, with no word of the control
// plane, and the wake-ups go with the line that ends their turn.
func TestAPromptIsTheOwnersOrTheControlPlanes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "also add docs\n\nand tests", promptText([]orchestration.ChatMessage{
		{Role: orchestration.ChatOwner, Text: "also add docs"},
		{Role: orchestration.ChatOwner, Text: "and tests"},
	}))
	assert.Equal(t, fmt.Sprint(
		"Message from the Hypomnema control plane: these need you.\n",
		"- Task t1 of run r1 is succeeded: hypomnema tasks show t1 --json\n",
		"Act on them, then end your turn; the control plane wakes you again.\n"),
		promptText([]orchestration.ChatMessage{
			{Role: orchestration.ChatSystem, Text: "Task t1 of run r1 is succeeded: hypomnema tasks show t1 --json"},
		}))
}

// scriptedPi plays pi in the test's own goroutine: it answers each command at once, a prompt by starting a turn,
// and keeps the prompts it was given.
type scriptedPi struct {
	t       *testing.T
	p       *proc
	prompts []string
}

func (w *scriptedPi) Write(line []byte) (int, error) {
	var cmd map[string]any
	if err := json.Unmarshal(line, &cmd); err != nil {
		return 0, err
	}

	data := json.RawMessage(`{}`)
	if cmd["type"] == "prompt" {
		w.prompts = append(w.prompts, fmt.Sprint(cmd["message"]))
		data = json.RawMessage(`{"disposition":"started"}`)
	}

	reply, err := json.Marshal(map[string]any{
		"type": "response", "id": cmd["id"], "command": cmd["type"], "success": true, "data": data,
	})
	if err != nil {
		return 0, err
	}

	w.p.handle(w.t.Context(), reply)

	return len(line), nil
}

func (*scriptedPi) Close() error { return nil }

// scripted is a supervisor that does not run, whose chat's pi is a scriptedPi, and a step that makes one pass of the
// chat as pi is idle and returns the prompt pi was given, or "", its turn settled.
func scripted(t *testing.T, svc *orchestration.Service, chatID string) func() string {
	t.Helper()

	sup := New(svc, Config{Command: []string{"pi"}})
	p := &proc{
		s: sup, chatID: chatID, token: "scripted", done: make(chan struct{}), lastActive: time.Now(),
		waiting: map[string]chan response{}, calls: map[string]json.RawMessage{},
	}
	pi := &scriptedPi{t: t, p: p}
	p.stdin = pi
	sup.procs[chatID] = p

	return func() string {
		t.Helper()

		before := len(pi.prompts)
		require.NoError(t, sup.deliver(t.Context(), chatID))

		if len(pi.prompts) == before {
			return ""
		}

		require.True(t, sup.Working(chatID))
		p.handle(t.Context(), []byte(`{"type":"agent_settled","aborted":false}`))

		return pi.prompts[len(pi.prompts)-1]
	}
}

// Scenario (issue #1172): the owner writes to a busy Bot whose Runs keep waking it. Each idle pi is given one kind,
// the one that waited longest: the wake-ups before the owner's message, then the message alone, with no word of the
// control plane, then the wake-up after it. An owner message that waited before any wake-up goes first.
func TestATurnHoldsOneKindInTheOrderTheyWaited(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)

	for name, ownerFirst := range map[string]bool{"wake-ups first": false, "the owner first": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, clock := newStore(t)
			ctx := t.Context()

			chat, err := svc.CreateChat(ctx, "", "")
			require.NoError(t, err)

			run, err := svc.CreateRun(ctx, orchestration.NewRun{Objective: "research", CreatedBy: "pi", ChatID: chat.ID})
			require.NoError(t, err)

			tasks, err := svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{
				{Ref: "a", Spec: "a"}, {Ref: "b", Spec: "b"}, {Ref: "c", Spec: "c"}, {Ref: "d", Spec: "d"},
			})
			require.NoError(t, err)

			at := func(minute int) { clock.set(start.Add(time.Duration(minute) * time.Minute)) }
			wake := func(minute int, task orchestration.Task) {
				at(minute)
				require.NoError(t, svc.CancelTask(ctx, task.ID))
				_, err := svc.WakeChats(ctx)
				require.NoError(t, err)
			}
			brief := func(minute int) {
				at(minute)
				_, queued, err := svc.SendOwnerMessage(ctx, chat.ID,
					orchestration.NewChatMessage{Role: orchestration.ChatOwner, Text: "answer the research brief"}, true)
				require.NoError(t, err)
				require.NotNil(t, queued)
			}

			if ownerFirst {
				brief(1)
				wake(2, tasks[0])
			} else {
				wake(1, tasks[0])
				wake(2, tasks[1])
				brief(3)
				wake(4, tasks[2])
			}

			at(10)

			next := scripted(t, svc, chat.ID)

			var got []string

			for prompt := next(); prompt != ""; prompt = next() {
				got = append(got, prompt)
			}

			for _, prompt := range got {
				if strings.Contains(prompt, "answer the research brief") {
					assert.Equal(t, "answer the research brief", prompt, "the owner's message is a turn of its own")

					continue
				}

				assert.Contains(t, prompt, "Message from the Hypomnema control plane: these need you.")
				assert.Contains(t, prompt, "Act on them, then end your turn")
			}

			mentions := func(prompt string, ts ...orchestration.Task) bool {
				for _, task := range tasks {
					if strings.Contains(prompt, "Task "+task.ID) != slices.Contains(ts, task) {
						return false
					}
				}

				return true
			}

			if ownerFirst {
				require.Len(t, got, 2)
				assert.Equal(t, "answer the research brief", got[0])
				assert.True(t, mentions(got[1], tasks[0]), got[1])
			} else {
				require.Len(t, got, 3)
				assert.True(t, mentions(got[0], tasks[0], tasks[1]), got[0])
				assert.Equal(t, "answer the research brief", got[1])
				assert.True(t, mentions(got[2], tasks[2]), got[2])
			}

			undelivered, err := svc.Undelivered(ctx, chat.ID)
			require.NoError(t, err)
			assert.Empty(t, undelivered)
		})
	}
}

// Scenario: the owner sends files (issue #343). pi gets the text and one line a file with its path in the Bot's
// folder, its working directory, which its tools read; a message of files alone is the lines.
func TestPiIsGivenThePathsOfTheOwnersFiles(t *testing.T) {
	t.Parallel()

	got := promptText([]orchestration.ChatMessage{
		{Role: orchestration.ChatOwner, Text: "summarize these", Attachments: []orchestration.ChatAttachment{
			{Name: "report.pdf", Path: "attachments/cht_1/report (2).pdf", Size: 1234, Type: "application/pdf"},
			{Name: "notes", Path: "attachments/cht_1/notes", Size: 5},
		}},
		{Role: orchestration.ChatOwner, Attachments: []orchestration.ChatAttachment{
			{Name: "a.png", Path: "attachments/cht_1/a.png", Size: 3, Type: "image/png"},
		}},
	})
	assert.Equal(t, fmt.Sprint(
		"summarize these\n\n",
		"The owner attached these files, in your working directory:\n",
		"- attachments/cht_1/report (2).pdf (application/pdf, 1234 bytes)\n",
		"- attachments/cht_1/notes (file, 5 bytes)",
		"\n\n",
		"The owner attached these files, in your working directory:\n",
		"- attachments/cht_1/a.png (image/png, 3 bytes)"), got)
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

	args, _, _, err := sup.botArgs(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"--bot", orchestration.DefaultBotID}, args, "the starting set leaves pi's own default model")

	_, err = svc.ApplyProvider(ctx, "", "name: ninerouter\nkind: openai_compatible\n"+
		"base_url: http://172.22.0.1:20128/v1\napi_key_ref: file:ninerouter.key\nmodels: [cx/gpt-6.1-sol]\n")
	require.NoError(t, err)

	vmapBot, err := svc.PutBot(ctx, orchestration.Bot{
		Name: "vmap", Provider: "ninerouter", Model: "cx/gpt-6.1-sol", Effort: "high", Persona: "You answer about VMAP.",
	})
	require.NoError(t, err)

	second, err := svc.CreateChat(ctx, vmapBot.ID, "")
	require.NoError(t, err)

	args, bot, model, err := sup.botArgs(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--bot", vmapBot.ID, "--provider", "ninerouter", "--model", "cx/gpt-6.1-sol", "--thinking", "high",
	}, args)
	assert.Equal(t, "cx/gpt-6.1-sol", model.Model, "the chat's gateway key is made for this model")
	assert.Equal(t, vmapBot.ID, bot, "pi's token is bound to the Bot")

	args, _, _, err = sup.botArgs(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"--bot", orchestration.DefaultBotID}, args, "the first chat keeps its Bot")

	// A Lark group's chat is not given the Bot's memory (issue #246); a person's direct chat is.
	_, err = svc.Channels().SetChannel(ctx, channelstore.Channel{
		Name: "maps", AppID: "cli_m4p5", Secret: "lark-channel-maps", Bot: vmapBot.ID,
		Mode: channelstore.ChannelOwner, OwnerID: "ou_owner", Enabled: true,
	})
	require.NoError(t, err)

	group, err := svc.ChannelGroupChatFor(ctx, "maps", "oc_group", "Lark: Maps team")
	require.NoError(t, err)

	args, _, _, err = sup.botArgs(ctx, group.ChatID)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--bot", vmapBot.ID, "--group", "--provider", "ninerouter", "--model", "cx/gpt-6.1-sol", "--thinking", "high",
	}, args)

	direct, err := svc.ChannelChatFor(ctx, "maps", "oc_dm", "ou_owner", "")
	require.NoError(t, err)

	args, _, _, err = sup.botArgs(ctx, direct.ChatID)
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

// Scenario: a chat is deleted (issue #484). Stop ends its idle pi and returns once it has exited, so its files can go:
// it holds no key then, and the key ends with it. A chat with no pi is stopped with nothing to do.
func TestStopEndsTheChatsPi(t *testing.T) {
	t.Parallel()

	keys := &fakeKeys{}
	svc, sup := newSupervisorWith(t, keys)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	sup.Stop(chat.ID)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hello")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: hello")
	require.Eventually(t, func() bool { return !sup.Working(chat.ID) }, 10*time.Second, 20*time.Millisecond)

	sup.Stop(chat.ID)

	_, ok := sup.Key(chat.ID)
	assert.False(t, ok, "pi exited before Stop returned")
	require.Eventually(t, func() bool { return keys.ended("gwk_1") }, 10*time.Second, 20*time.Millisecond,
		"the key ends with the pi")
}

// answerAndExit plays a pi that answers each command and exits at once: its
// response is handed over and then done closes, as the reader does.
type answerAndExit struct{ p *proc }

func (w answerAndExit) Write(line []byte) (int, error) {
	var cmd map[string]any
	if err := json.Unmarshal(line, &cmd); err != nil {
		return 0, err
	}

	w.p.mu.Lock()
	ch := w.p.waiting[fmt.Sprint(cmd["id"])]
	w.p.mu.Unlock()

	ch <- response{Success: true, Data: json.RawMessage(`{"disposition":"started"}`)}
	close(w.p.done)

	return len(line), nil
}

func (answerAndExit) Close() error { return nil }

// Scenario: pi accepts a prompt and exits in its turn (issue #459). The
// prompt was accepted, so it is not given again to another pi; a call that
// took the exit over the answer did so about half the time.
func TestAPromptPiAcceptedBeforeItExitedIsAccepted(t *testing.T) {
	t.Parallel()

	for range 100 {
		p := &proc{done: make(chan struct{}), waiting: map[string]chan response{}}
		p.stdin = answerAndExit{p}

		r, err := p.call(t.Context(), map[string]any{"type": "prompt", "message": "crash please"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"disposition":"started"}`, string(r.Data))
	}
}

// fakeTokens stands for the control plane's tokens of pi processes: it
// hands one out per pi and remembers which it was asked to end.
type fakeTokens struct {
	mu      sync.Mutex
	made    int
	revoked map[string]bool
}

func (f *fakeTokens) AddBotToken(_ context.Context, chatID, bot string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.made++

	return fmt.Sprintf("hyp_bot_v1_%d_%s_%s", f.made, chatID, bot), nil
}

func (f *fakeTokens) RevokeBotToken(secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.revoked == nil {
		f.revoked = map[string]bool{}
	}

	f.revoked[secret] = true
}

func (f *fakeTokens) ended(secret string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.revoked[secret]
}

// Scenario: each pi gets a token of its own, bound to its chat and its Bot
// (issue #260). The launcher reads it as the first line of pi's stdin,
// before any command; it ends with that pi, and the pi started after it gets
// another.
func TestEachPiGetsItsOwnTokenOnItsStdin(t *testing.T) {
	t.Parallel()

	tokens := &fakeTokens{}
	svc, _ := newSupervisorConfig(t, Config{Tokens: tokens})
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	first := "hyp_bot_v1_1_" + chat.ID + "_" + orchestration.DefaultBotID

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "what is your token?")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "my token: "+first)
	assert.False(t, tokens.ended(first))

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "crash please")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return tokens.ended(first) }, 10*time.Second, 20*time.Millisecond,
		"the token ends with the pi it was made for")

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "and your token now?")
	require.NoError(t, err)
	token := "my token: hyp_bot_v1_2_" + chat.ID + "_" + orchestration.DefaultBotID
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, token)
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

// A chat's own model and effort (issue #369) are pi's flags when it starts: what the chat chose, and its Bot's for
// what it did not. The gateway's effort none is pi's thinking level off.
func TestPiStartsWithTheChatsModel(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	_, err := svc.ApplyProvider(ctx, "", "name: gateway\nkind: openai_compatible\n"+
		"base_url: http://127.0.0.1:8080/v1\napi_key_ref: file:gateway.key\nmodels: [gpt-sol, gpt-luna]\n")
	require.NoError(t, err)

	vmapBot, err := svc.PutBot(ctx, orchestration.Bot{Name: "vmap", Provider: "gateway", Model: "gpt-sol", Effort: "high"})
	require.NoError(t, err)

	effortOnly, err := svc.CreateChatWithID(ctx, "", vmapBot.ID, "", orchestration.ChatModel{Effort: "none"})
	require.NoError(t, err)

	args, _, model, err := sup.botArgs(ctx, effortOnly.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"--bot", vmapBot.ID, "--provider", "gateway", "--model", "gpt-sol", "--thinking", "off"},
		args, "the Bot's model with the chat's effort")
	assert.Equal(t, "gpt-sol", model.Model)

	both, err := svc.CreateChatWithID(ctx, "", vmapBot.ID, "",
		orchestration.ChatModel{Provider: "gateway", Model: "gpt-luna", Effort: "low"})
	require.NoError(t, err)

	args, _, model, err = sup.botArgs(ctx, both.ID)
	require.NoError(t, err)
	want := []string{"--bot", vmapBot.ID, "--provider", "gateway", "--model", "gpt-luna", "--thinking", "low"}
	assert.Equal(t, want, args)
	assert.Equal(t, "gpt-luna", model.Model, "the chat's gateway key is made for the chat's model")
}

func TestBotFallbacksUseChatCompletionsOnlyForTheInheritedModel(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()
	bot, err := svc.PutBot(ctx, orchestration.Bot{
		Name: "researcher", Provider: "codex-subscription", Model: "gpt-6.1-sol",
		Fallbacks: []string{"claude-subscription/claude-opus-5-5"},
	})
	require.NoError(t, err)
	for _, candidate := range []struct {
		choice   orchestration.ChatModel
		provider string
	}{
		{orchestration.ChatModel{Effort: "high"}, orchestration.PiFallbackProvider},
		{orchestration.ChatModel{Provider: bot.Provider, Model: bot.Model}, orchestration.PiProvider},
	} {
		chat, err := svc.CreateChatWithID(ctx, "", bot.ID, "", candidate.choice)
		require.NoError(t, err)
		_, model, err := sup.chatModel(ctx, chat.ID)
		require.NoError(t, err)
		assert.Equal(t, candidate.provider, model.Provider)
		assert.Equal(t, "codex-subscription/gpt-6.1-sol", model.Model)
	}
}

// Scenario: the owner chooses another model and effort in a chat whose pi runs (issue #369). pi is not started
// again: before the next message it is sent set_model and set_thinking_level, and the chat's gateway key, good for
// one model, is made again for the new one. A change of the effort alone sends only the level.
func TestAChangedModelReachesARunningPi(t *testing.T) {
	t.Parallel()

	keys := &fakeKeys{}
	svc, sup := newSupervisorWith(t, keys)
	ctx := t.Context()

	_, err := svc.ApplyProvider(ctx, "", "name: gateway\nkind: openai_compatible\n"+
		"base_url: http://127.0.0.1:8080/v1\napi_key_ref: file:gateway.key\nmodels: [gpt-sol, gpt-luna]\n")
	require.NoError(t, err)

	vmapBot, err := svc.PutBot(ctx, orchestration.Bot{Name: "vmap", Provider: "gateway", Model: "gpt-sol", Effort: "high"})
	require.NoError(t, err)

	chat, err := svc.CreateChat(ctx, vmapBot.ID, "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "which model first")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "model gateway/gpt-sol thinking high")

	first, ok := sup.Key(chat.ID)
	require.True(t, ok)
	require.Eventually(t, func() bool { return !sup.Working(chat.ID) }, 5*time.Second, 20*time.Millisecond)

	luna, xhigh, gateway := "gpt-luna", "xhigh", "gateway"
	_, err = svc.SetChatModel(ctx, chat.ID,
		orchestration.ChatModelChange{Provider: &gateway, Model: &luna, Effort: &xhigh})
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "which model now")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "model gateway/gpt-luna thinking xhigh")

	second, ok := sup.Key(chat.ID)
	require.True(t, ok)
	assert.NotEqual(t, first, second, "pi asks for a key good for the new model")

	keys.mu.Lock()
	assert.Equal(t, []string{"gpt-sol", "gpt-luna"}, keys.models, "one pi: a key at its start, one for the change")
	keys.mu.Unlock()

	require.Eventually(t, func() bool { return !sup.Working(chat.ID) }, 5*time.Second, 20*time.Millisecond)

	none := "none"
	_, err = svc.SetChatModel(ctx, chat.ID, orchestration.ChatModelChange{Effort: &none})
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "which model at last")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "model gateway/gpt-luna thinking off")

	keys.mu.Lock()
	assert.Len(t, keys.models, 2, "the same model keeps its key")
	keys.mu.Unlock()
}

// A message written while pi works waits in the chat's queue, off the transcript, and goes to pi as a new prompt
// once the turn settles (issue #497); one sent now goes at once as a steer. Wake-ups are not steered. The owner's
// stop takes the steer pi had not read out of its queue, and it goes again with the queued one, the owner's next
// turn (issue #1172).
func TestTheQueueWaitsForTheTurnAndSendNowSteers(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hang aborted")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)

	_, later, err := svc.SendOwnerMessage(ctx, chat.ID,
		orchestration.NewChatMessage{Role: orchestration.ChatOwner, Text: "later"}, sup.Working(chat.ID))
	require.NoError(t, err)
	require.NotNil(t, later)

	sendNow(t, svc, sup, chat.ID, "look here")

	// The steer is given while pi still works; the other stays queued.
	require.Eventually(t, func() bool { return heldSteers(sup, chat.ID) == 1 }, 10*time.Second, 10*time.Millisecond)

	queue, err := svc.ChatQueue(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, queue, 1)
	assert.Equal(t, "later", queue[0].Text)
	assert.True(t, sup.Working(chat.ID))

	require.NoError(t, sup.Abort(ctx, chat.ID))
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: look here")

	assert.Equal(t, []string{
		"owner: hang aborted", "owner: look here", "coordinator: half an answer", "system: Stopped",
		"owner: later", "coordinator: echo: look here",
	}, said(t, svc, chat.ID), "the steer goes again once, as the owner's next turn, and pi does not hold it")

	queue, err = svc.ChatQueue(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, queue)
}

// Scenario (issue #1192): a pass read pi as working, and the owner's stop settled before it sends. The steer the stop
// dropped is no longer given, but pi works no more: it is given nothing as a steer, and goes with the queue as the
// owner's next turn (nextTurn), not alone ahead of it.
func TestASettledPiIsGivenNoSteer(t *testing.T) {
	t.Parallel()

	dropped := []orchestration.ChatMessage{{Seq: 2, Role: orchestration.ChatOwner, Text: "look here"}}

	p := &proc{working: true}
	assert.Len(t, p.unsent(dropped), 1, "a pi that works is steered with a row it was not given")

	p.working = false
	assert.Empty(t, p.unsent(dropped), "a pi whose turn settled is given no steer")
}

// sendNow writes an owner message while pi works, which queues it, and sends it now, which steers pi with it.
func sendNow(t *testing.T, svc *orchestration.Service, sup *Supervisor, chatID, text string) {
	t.Helper()

	_, queued, err := svc.SendOwnerMessage(t.Context(), chatID,
		orchestration.NewChatMessage{Role: orchestration.ChatOwner, Text: text}, sup.Working(chatID))
	require.NoError(t, err)
	require.NotNil(t, queued)

	_, err = svc.SendQueuedNow(t.Context(), chatID, queued.ID)
	require.NoError(t, err)
}

// heldSteers is how many steers the chat's pi queued and has not read.
func heldSteers(sup *Supervisor, chatID string) int {
	sup.mu.Lock()
	p := sup.procs[chatID]
	sup.mu.Unlock()

	if p == nil {
		return 0
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.unread)
}

// said is the chat's messages with text, but its tool calls, as "role: text".
func said(t *testing.T, svc *orchestration.Service, chatID string) []string {
	t.Helper()

	msgs, err := svc.ListChatMessages(t.Context(), chatID, 0, 0)
	require.NoError(t, err)

	var got []string

	for _, m := range msgs {
		// The tool calls and the thinking before them are other tests' business.
		if m.Role != orchestration.ChatTool && m.Text != "" {
			got = append(got, fmt.Sprintf("%s: %s", m.Role, m.Text))
		}
	}

	return got
}

// undelivered is the text of the chat's rows pi has not had.
func undelivered(t *testing.T, svc *orchestration.Service, chatID string) []string {
	t.Helper()

	msgs, err := svc.Undelivered(t.Context(), chatID)
	require.NoError(t, err)

	out := []string{}
	for _, m := range msgs {
		out = append(out, m.Text)
	}

	return out
}

// Scenario (issue #1172): pi queues a steer and reads it later in its turn. Its row waits until pi read it, as t3code
// counts a steer read at pi's user message (adapter.ts), and a pass meanwhile does not give it again.
func TestASteerIsDeliveredOncePiReadsIt(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hang settled")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)

	sendNow(t, svc, sup, chat.ID, "look here")
	require.Eventually(t, func() bool { return heldSteers(sup, chat.ID) == 1 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"look here"}, undelivered(t, svc, chat.ID), "pi has not read it yet")

	// The pass this message makes is given only it, not the steer pi holds.
	sendNow(t, svc, sup, chat.ID, "finish now")
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "read: ")

	require.Eventually(t, func() bool { return len(undelivered(t, svc, chat.ID)) == 0 }, 10*time.Second,
		10*time.Millisecond)
	assert.Equal(t, []string{
		"owner: hang settled", "owner: look here", "owner: finish now", "coordinator: read: look here | finish now",
	}, said(t, svc, chat.ID), "one steer a row")
}

// Scenario (issue #1172): pi queues a steer and exits before it read it. The row stays undelivered, and the next pi
// is given it as a prompt, as OpenClaw gives a steer the session never read again (attempt-queue-message.ts).
func TestASteerAPiNeverReadGoesToTheNextPi(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hang settled")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)

	sendNow(t, svc, sup, chat.ID, "look here and exit now")
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: look here and exit now")

	assert.Equal(t, []string{
		"owner: hang settled", "owner: look here and exit now", "coordinator: echo: look here and exit now",
	}, said(t, svc, chat.ID))
}

// Scenario (issue #1172): the owner stops a turn while pi holds a steer it has not read. The stop clears pi's queue
// before the abort, so pi does not take the steer in after its next prompt, and the row goes again as a turn of its
// own; the fake pi still holds a steer an abort came before the clear.
func TestAStopGivesAnUnreadSteerAgainAsItsOwnTurn(t *testing.T) {
	t.Parallel()

	svc, sup := newSupervisor(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hang settled")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)

	sendNow(t, svc, sup, chat.ID, "look here")
	require.Eventually(t, func() bool { return heldSteers(sup, chat.ID) == 1 }, 10*time.Second, 10*time.Millisecond)

	require.NoError(t, sup.Abort(ctx, chat.ID))
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: look here")

	assert.Equal(t, []string{
		"owner: hang settled", "owner: look here", "system: Stopped", "coordinator: echo: look here",
	}, said(t, svc, chat.ID))
}
