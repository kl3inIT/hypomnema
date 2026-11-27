package workertools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/toolsmcp"
)

// client speaks MCP to a server over pipes, as Claude Code does over stdio.
type client struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	dir string
}

func newClient(t *testing.T) *client {
	t.Helper()

	return newClientIn(t, t.TempDir())
}

// newClientIn is a client of the server over the bridge directory dir, with
// what its runner recorded there.
func newClientIn(t *testing.T, dir string) *client {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	s := &server{dir: dir, poll: 10 * time.Millisecond, progress: 30 * time.Millisecond}
	s.agent, s.worktree = agentOf(dir)

	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = toolsmcp.Serve(context.WithoutCancel(t.Context()), inR, outW, s.config())
		_ = outW.Close()
	}()

	t.Cleanup(func() {
		_ = inW.Close()
		<-done
	})

	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	return &client{t: t, in: inW, out: sc, dir: dir}
}

func (c *client) send(msg map[string]any) {
	c.t.Helper()

	raw, err := json.Marshal(msg)
	require.NoError(c.t, err)

	_, err = c.in.Write(append(raw, '\n'))
	require.NoError(c.t, err)
}

func (c *client) read() map[string]any {
	c.t.Helper()

	require.True(c.t, c.out.Scan(), "the server answers")

	var msg map[string]any
	require.NoError(c.t, json.Unmarshal(c.out.Bytes(), &msg))

	return msg
}

func text(t *testing.T, msg map[string]any) string {
	t.Helper()

	result, ok := msg["result"].(map[string]any)
	require.True(t, ok, "a result: %v", msg)

	content, ok := result["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)

	first, ok := content[0].(map[string]any)
	require.True(t, ok)

	s, ok := first["text"].(string)
	require.True(t, ok)

	return s
}

// Scenario: Claude Code connects, lists the tools, and the worker finishes
// with a Gate. The report lands in the bridge for the runner.
func TestFinishIsRecordedInTheBridge(t *testing.T) {
	t.Parallel()

	c := newClient(t)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18"}})
	init := c.read()
	assert.Equal(t, "2025-06-18", init["result"].(map[string]any)["protocolVersion"]) //nolint:forcetypeassert // test

	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})

	list := c.read()
	tools, ok := list["result"].(map[string]any)["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 2)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
		"name": "finish", "arguments": map[string]any{
			"summary":  "added the button; PR #7 is green",
			"evidence": []map[string]string{{"kind": "pull_request", "ref": "kl3inIT/second-bite#7"}},
			"gate": map[string]any{"question": "Merge PR #7?", "options": []any{
				"merge", map[string]any{"label": "change first", "allows_text": true},
			}},
		},
	}})
	assert.Contains(t, text(t, c.read()), "End your turn now")

	final, ok, err := bridge.Finished(c.dir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "added the button; PR #7 is green", final.Summary)
	require.NotNil(t, final.Gate)
	assert.Equal(t, "Merge PR #7?", final.Gate.Text)
	assert.Equal(t, "owner", final.Gate.To)
	assert.Equal(t, []nodeproto.Option{
		{Label: "merge"}, {Label: "change first", AllowsText: true}, {Label: "Other", AllowsText: true},
	}, final.Gate.Options, "an option keeps its text, and Other takes text")

	c.send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{
		"name": "finish", "arguments": map[string]any{},
	}})
	assert.Contains(t, text(t, c.read()), "needs a summary")
}

// Scenario: the worker asks and waits. Progress notifications keep the call
// alive while it waits; the answer the runner writes ends the call.
func TestAskWaitsWithProgressUntilAnswered(t *testing.T) {
	t.Parallel()

	c := newClient(t)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{
		"name": "ask", "arguments": map[string]any{"question": "goth or hand-written?", "options": []string{"goth", "x"}},
		"_meta": map[string]any{"progressToken": "tok_1"},
	}})

	progress := c.read()
	assert.Equal(t, "notifications/progress", progress["method"])
	assert.Equal(t, "tok_1", progress["params"].(map[string]any)["progressToken"]) //nolint:forcetypeassert // test

	qs, err := bridge.Questions(c.dir)
	require.NoError(t, err)
	require.Len(t, qs, 1)
	assert.Equal(t, "goth or hand-written?", qs[0].Ask.Text)
	assert.Equal(t, "owner", qs[0].Ask.To)

	require.NoError(t, bridge.Answer(c.dir, qs[0].Seq, "goth"))

	for {
		msg := c.read()
		if msg["method"] == "notifications/progress" {
			continue
		}

		assert.InDelta(t, 7, msg["id"], 0)
		assert.Equal(t, "The answer: goth", text(t, msg))

		break
	}
}

// Scenario: Claude Code cancels a waiting ask (an interrupt). The call ends
// at once, saying the question was withdrawn.
func TestCancelledAskEnds(t *testing.T) {
	t.Parallel()

	c := newClient(t)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{
		"name": "ask", "arguments": map[string]any{"question": "anything?"},
	}})
	//nolint:misspell // the MCP method is spelled this way
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 9}})

	assert.Equal(t, "The question was withdrawn.", text(t, c.read()))
}

// Scenario: a worker's options, as the ask and finish tools take them
// (issue #714, decision 8), through the tools' own calls. A plain label and
// {label, allows_text} are both read, and a blank label is dropped. Every
// question with options ends with an "Other" that takes text: added when the
// worker gave none, and made to take text when the worker gave one without.
// A question that would pass the control plane's 10 options with "Other" is
// refused, so the worker cuts one, rather than one being dropped or "Other"
// left out. A question without options stays a free answer.
func TestOptionsCarryText(t *testing.T) {
	t.Parallel()

	gate := func(t *testing.T, options any) ([]nodeproto.Option, string) {
		t.Helper()

		c := newClient(t)
		c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
			"name": "finish", "arguments": map[string]any{
				"summary": "PR #7 is green", "gate": map[string]any{"question": "Merge PR #7?", "options": options},
			},
		}})
		reply := text(t, c.read())

		final, ok, err := bridge.Finished(c.dir)
		require.NoError(t, err)

		if !ok || final.Gate == nil {
			return nil, reply
		}

		return final.Gate.Options, reply
	}

	got, _ := gate(t, []any{"merge", " ", map[string]any{"label": "change first", "allows_text": true}})
	assert.Equal(t, []nodeproto.Option{
		{Label: "merge"}, {Label: "change first", AllowsText: true}, {Label: "Other", AllowsText: true},
	}, got)

	got, _ = gate(t, []string{"Yes", "other", "No"})
	assert.Equal(t, []nodeproto.Option{{Label: "Yes"}, {Label: "No"}, {Label: "Other", AllowsText: true}}, got,
		"the worker's own Other takes text, and comes last")

	nine := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
	got, _ = gate(t, nine)
	require.Len(t, got, maxOptions)
	assert.Equal(t, nodeproto.Option{Label: "Other", AllowsText: true}, got[maxOptions-1])

	got, reply := gate(t, append(nine, "10"))
	assert.Nil(t, got, "nothing is recorded")
	assert.Contains(t, reply, "at most 9 options")

	c := newClient(t)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "ask", "arguments": map[string]any{"question": "which?", "options": append(nine, "10")},
	}})
	assert.Contains(t, text(t, c.read()), "at most 9 options")
	qs, err := bridge.Questions(c.dir)
	require.NoError(t, err)
	assert.Empty(t, qs, "a refused question is not asked")

	got, _ = gate(t, []string{})
	assert.Empty(t, got, "no options: a free answer")

	var bad []option
	require.Error(t, json.Unmarshal([]byte(`[3]`), &bad))
}

// Scenario: a worker stops at a Gate that holds two decisions, each with a
// header and options that say what they change (issue #1219, decisions 1-3).
// The Gate keeps the first decision where a control plane before the issue
// reads it, and the second in More; each decision's options end with "Other".
// More than three decisions, or a decision without a header, is refused, so
// the worker splits or names it rather than the owner reading a bare list.
// `ask` takes the same shape.
func TestAGateAsksSeveralDecisionsThatStandAlone(t *testing.T) {
	t.Parallel()

	decision := func(header, question string) map[string]any {
		return map[string]any{"header": header, "question": question, "options": []any{
			map[string]any{"label": "One item each (Recommended)", "description": "Each request is its own item. From: Linear."},
			map[string]any{"label": "One item for all", "description": "No reference does this."},
		}}
	}

	finish := func(t *testing.T, questions ...map[string]any) (*nodeproto.Ask, string) {
		t.Helper()

		c := partsClient(t)
		c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
			"name": "finish", "arguments": map[string]any{
				"summary": "the design is in the issue", "gate": map[string]any{"questions": questions},
			},
		}})
		reply := text(t, c.read())

		final, ok, err := bridge.Finished(c.dir)
		require.NoError(t, err)

		if !ok {
			return nil, reply
		}

		return final.Gate, reply
	}

	gate, _ := finish(t, decision(" Items ", "Is each request one item?"), decision("Server", "Fix the server now?"))
	require.NotNil(t, gate)

	options := []nodeproto.Option{
		{Label: "One item each (Recommended)", Description: "Each request is its own item. From: Linear."},
		{Label: "One item for all", Description: "No reference does this."},
		{Label: "Other", AllowsText: true},
	}
	assert.Equal(t, nodeproto.Ask{
		Header: "Items", Text: "Is each request one item?", Options: options, To: "owner",
		More: []nodeproto.Part{{Header: "Server", Text: "Fix the server now?", Options: options}},
	}, *gate)

	gate, reply := finish(t, decision("A", "1?"), decision("B", "2?"), decision("C", "3?"), decision("D", "4?"))
	assert.Nil(t, gate, "nothing is recorded")
	assert.Contains(t, reply, "at most 3 decisions")

	_, reply = finish(t, decision("", "Which?"))
	assert.Contains(t, reply, "needs a header")

	_, reply = finish(t, decision("A header that runs past thirty characters", "Which?"))
	assert.Contains(t, reply, "header of at most 30 characters")

	c := partsClient(t)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "ask", "arguments": map[string]any{"questions": []any{decision("Items", "One item each?")}},
	}})

	require.Eventually(t, func() bool {
		qs, err := bridge.Questions(c.dir)

		return err == nil && len(qs) == 1
	}, 5*time.Second, 10*time.Millisecond)

	qs, err := bridge.Questions(c.dir)
	require.NoError(t, err)
	assert.Equal(t, nodeproto.Ask{Header: "Items", Text: "One item each?", Options: options, To: "owner"}, qs[0].Ask)

	require.NoError(t, bridge.Answer(c.dir, qs[0].Seq, "Items: One item each"))
	assert.Equal(t, "The answer: Items: One item each", text(t, c.read()))
}

// partsClient is a client of a worker whose Dispatch came from a control plane that keeps several decisions
// (Offer.QuestionParts, issue #1219).
func partsClient(t *testing.T) *client {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, bridge.EnableQuestionParts(dir))

	return newClientIn(t, dir)
}

// schemaOf is a listed tool's input schema, by its name.
func schemaOf(t *testing.T, c *client, name string) map[string]any {
	t.Helper()

	c.send(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "tools/list"})

	tools, ok := c.read()["result"].(map[string]any)["tools"].([]any)
	require.True(t, ok)

	for _, tool := range tools {
		if m, _ := tool.(map[string]any); m["name"] == name {
			schema, _ := m["inputSchema"].(map[string]any)

			return schema
		}
	}

	require.FailNow(t, "no tool "+name)

	return nil
}

// Scenario: review of PR #1228. A node takes a release before its control plane does, so a worker may run under a
// control plane that keeps one question only. Its tools then offer ask and finish as that control plane takes them,
// one question with labels; several decisions are refused rather than all but the first being dropped, and a
// header or an option's description is written into the question's text, which that control plane keeps. Under a
// control plane that says it keeps them (Offer.QuestionParts), ask and finish both offer the decisions.
func TestAWorkerAsksSeveralDecisionsOnlyOfAControlPlaneThatKeepsThem(t *testing.T) {
	t.Parallel()

	required := func(schema map[string]any) any { return schema["required"] }
	gateOf := func(schema map[string]any) map[string]any {
		gate, _ := schema["properties"].(map[string]any)["gate"].(map[string]any)

		return gate
	}

	old := newClient(t)
	assert.Equal(t, []any{"question"}, required(schemaOf(t, old, "ask")))
	assert.Equal(t, []any{"question"}, gateOf(schemaOf(t, old, "finish"))["required"])

	current := partsClient(t)
	assert.Equal(t, []any{"questions"}, required(schemaOf(t, current, "ask")), "ask offers the decisions too")
	assert.Equal(t, []any{"questions"}, gateOf(schemaOf(t, current, "finish"))["required"])

	decision := func(header string) map[string]any {
		return map[string]any{"header": header, "question": header + "?", "options": []any{
			map[string]any{"label": "Yes", "description": "Do it now. From: Linear."}, "No",
		}}
	}

	old.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "finish", "arguments": map[string]any{
			"summary": "planned", "gate": map[string]any{"questions": []any{decision("Items"), decision("Server")}},
		},
	}})
	assert.Contains(t, text(t, old.read()), "one question at a time")

	_, ok, err := bridge.Finished(old.dir)
	require.NoError(t, err)
	assert.False(t, ok, "nothing is recorded")

	final, err := ParseFinish(json.RawMessage(`{"summary":"planned","gate":{"questions":[`+
		`{"header":"Items","question":"One item each?","options":[{"label":"Yes","description":"Do it now."},"No"]}]}}`),
		false)
	require.NoError(t, err)
	assert.Equal(t, &nodeproto.Ask{
		Text: "Items: One item each?\n1. Yes: Do it now.", To: "owner",
		Options: []nodeproto.Option{{Label: "Yes"}, {Label: "No"}, {Label: "Other", AllowsText: true}},
	}, final.Gate, "the header and the description are kept in the text")
}

// Scenario (issue #1220, review round 1 of PR #1236): the node's own tools stay with the old routes gone. A worker
// whose Task may push lists ask, finish and publish_branch first, and calls each: the question waits for the
// runner's answer, the finish lands in the bridge, and the push is asked of the node, whose answer ends the call.
func TestTheNodesOwnToolsAreListedAndCalled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, bridge.EnablePublish(dir))

	c := newClientIn(t, dir)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})

	tools, ok := c.read()["result"].(map[string]any)["tools"].([]any)
	require.True(t, ok)

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string)) //nolint:forcetypeassert // test
	}

	assert.Equal(t, []string{"ask", "finish", "publish_branch"}, names, "without the agent API, only its own")

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "ask", "arguments": map[string]any{"question": "which base?"},
	}})

	var qs []bridge.Question

	require.Eventually(t, func() bool {
		var err error
		qs, err = bridge.Questions(dir)

		return err == nil && len(qs) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, bridge.Answer(dir, qs[0].Seq, "main"))

	for msg := c.read(); ; msg = c.read() {
		if msg["method"] == "notifications/progress" {
			continue
		}

		assert.Equal(t, "The answer: main", text(t, msg))

		break
	}

	c.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
		"name": "publish_branch", "arguments": map[string]any{},
	}})

	var requests []string

	require.Eventually(t, func() bool {
		var err error
		requests, err = bridge.PublishRequests(dir)

		return err == nil && len(requests) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, bridge.CompletePublish(dir, requests[0], bridge.PublishResult{Message: "Pushed at abc123."}))
	assert.Equal(t, "Pushed at abc123.", text(t, c.read()))

	c.send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{
		"name": "finish", "arguments": map[string]any{"summary": "done"},
	}})
	assert.Contains(t, text(t, c.read()), "End your turn now")

	final, ok, err := bridge.Finished(dir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "done", final.Summary)
}
