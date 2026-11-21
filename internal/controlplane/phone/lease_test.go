package phone

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// filmNode stands for the node that holds the phone as it films: it keeps
// each take's state as take.go does, and records the calls it was sent in
// order, inputs of the owner's view included.
type filmNode struct {
	*fakeLink

	mu    sync.Mutex
	takes map[string]*devicetool.Take
	order []string
	// endHeld, when set, holds the node's answer to video_end until it is closed; endArrived is told it came.
	endHeld    chan struct{}
	endArrived chan struct{}
}

func newFilmNode() *filmNode {
	n := &filmNode{fakeLink: newLink(), takes: map[string]*devicetool.Take{}}
	n.connect("n-phone")
	n.said("n-phone", nodeproto.DeviceRunning)

	return n
}

func (n *filmNode) log(s string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.order = append(n.order, s)
}

func (n *filmNode) seen() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]string(nil), n.order...)
}

func (n *filmNode) DeviceCall(ctx context.Context, nodeID, agent, tool string, raw json.RawMessage,
) (nodeproto.DeviceAnswer, error) {
	if !devicetool.IsVideo(tool) {
		n.log(agent + " " + tool)

		return n.fakeLink.DeviceCall(ctx, nodeID, agent, tool, raw)
	}

	c, err := devicetool.ReadNode(tool, raw)
	if err != nil {
		return failed(err.Error()), nil
	}

	n.mu.Lock()
	held, arrived := n.endHeld, n.endArrived
	n.mu.Unlock()

	if tool == devicetool.VideoEnd && held != nil {
		close(arrived)
		<-held
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	n.order = append(n.order, agent+" "+tool+" "+c.Take)

	t := n.takes[c.Take]
	if tool == devicetool.VideoStart && t == nil {
		t = &devicetool.Take{ID: c.Take, Title: c.Title, Agent: agent, RequestID: c.RequestID,
			State: devicetool.TakeRecording}
		n.takes[c.Take] = t
	}

	if t == nil {
		return failed("no such take"), nil
	}

	switch tool {
	case devicetool.VideoSay:
		t.Steps = append(t.Steps, devicetool.TakeStep{AtMs: int64(len(t.Steps)) * 1000, Text: c.Text})
	case devicetool.VideoPause:
		if t.State == devicetool.TakeRecording {
			t.State = devicetool.TakePaused
		}
	case devicetool.VideoResume:
		if t.State == devicetool.TakePaused {
			t.State = devicetool.TakeRecording
		}
	case devicetool.VideoStop, devicetool.VideoEnd:
		if t.Running() {
			t.State, t.EndedBy = devicetool.TakeFinalizing, c.EndedBy
			if tool == devicetool.VideoStop {
				t.EndedBy = devicetool.EndedStop
			}
		}
	}

	out := *t

	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: "ok"}}, Take: &out}, nil
}

func failed(why string) nodeproto.DeviceAnswer {
	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: why}}, IsError: true}
}

// memTakes is the control plane's takes in memory.
type memTakes struct {
	mu    sync.Mutex
	takes map[string]devicetool.Take
}

func (s *memTakes) Create(t devicetool.Take) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.takes[t.ID] = t

	return nil
}

func (s *memTakes) Get(id string) (devicetool.Take, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.takes[id]
	if !ok {
		return devicetool.Take{}, ErrNoTake
	}

	return t, nil
}

func (s *memTakes) Update(said devicetool.Take) (devicetool.Take, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.takes[said.ID]
	if !ok {
		return devicetool.Take{}, ErrNoTake
	}

	// As package takes merges: what is said is kept, and an ended take is never running again.
	if said.State != "" && (t.Running() || !said.Running()) {
		t.State = said.State
	}

	if said.EndedBy != "" {
		t.EndedBy = said.EndedBy
	}

	if said.Steps != nil {
		t.Steps = said.Steps
	}

	s.takes[said.ID] = t

	return t, nil
}

func (s *memTakes) Deleted(string) bool { return false }

func (s *memTakes) List() []devicetool.Take {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]devicetool.Take, 0, len(s.takes))
	for _, t := range s.takes {
		out = append(out, t)
	}

	slices.SortFunc(out, func(a, b devicetool.Take) int { return b.StartedAt.Compare(a.StartedAt) })

	return out
}

func (s *memTakes) ByRequest(agent, requestID string) (devicetool.Take, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range s.takes {
		if t.Agent == agent && t.RequestID == requestID {
			return t, true
		}
	}

	return devicetool.Take{}, false
}

// filmRig is a Phone on a filming node, whose agents' work goes on until the
// test ends it.
type filmRig struct {
	p     *Phone
	node  *filmNode
	takes *memTakes
	end   *fakeEnd

	mu    sync.Mutex
	ended map[string]bool
}

func newFilmRig(t *testing.T) *filmRig {
	t.Helper()

	r := &filmRig{
		node: newFilmNode(), takes: &memTakes{takes: map[string]devicetool.Take{}}, end: newEnd(),
		ended: map[string]bool{},
	}
	r.p = New(Config{
		Nodes: &fakeNodes{nodes: []orchestration.Node{phoneNode("")}}, Link: r.node, Takes: r.takes,
		Views: func(context.Context, string) (ViewEnd, error) {
			return loggedEnd{fakeEnd: r.end, node: r.node}, nil
		},
		WorkAlive: func(_ context.Context, work string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()

			return !r.ended[work]
		},
		WakeWithin: 300 * time.Millisecond, Poll: 5 * time.Millisecond,
	})

	return r
}

// loggedEnd is the view's end, logging the owner's input among the node's calls.
type loggedEnd struct {
	*fakeEnd
	node *filmNode
}

func (e loggedEnd) Send(ctx context.Context, in nodeproto.DeviceViewMessage) error {
	e.node.log("owner " + in.Type)

	return e.fakeEnd.Send(ctx, in)
}

func (r *filmRig) endWork(work string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.ended[work] = true
}

// call is a tool's call by an agent: its text, and whether it failed.
func (r *filmRig) call(t *testing.T, c Caller, tool, args string) (string, bool) {
	t.Helper()

	raw, err := r.p.Call(t.Context(), c, tool, json.RawMessage(args))
	require.NoError(t, err)

	var answer nodeproto.DeviceAnswer
	require.NoError(t, json.Unmarshal(raw, &answer))
	assert.Nil(t, answer.Take, "an agent is never given the take itself")

	var text []string
	for _, part := range answer.Content {
		text = append(text, part.Text)
	}

	return strings.Join(text, " "), answer.IsError
}

// start starts a take for an agent and returns its id.
func (r *filmRig) start(t *testing.T, c Caller, request string) string {
	t.Helper()

	text, failed := r.call(t, c, devicetool.VideoStart, `{"title":"Sign in","request_id":"`+request+`"}`)
	require.False(t, failed, text)

	f, ok := r.p.Filming()
	require.True(t, ok)

	return f.Take
}

var (
	alice = Caller{Key: "d-1", Name: "dispatch 1", Work: "dispatch 1"}
	bob   = Caller{Key: "d-2", Name: "dispatch 2", Work: "dispatch 2"}
)

// Scenario: an agent films the phone. While its take runs, another agent's
// phone calls and starts are refused, saying for whom it films, and reach no
// node; the agent's own go through. After video_stop the other may use it.
func TestATakeHoldsThePhoneForItsAgent(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	take := r.start(t, alice, "a1")
	assert.True(t, strings.HasPrefix(take, "tk_"))

	text, failed := r.call(t, bob, devicetool.Screen, `{}`)
	assert.True(t, failed)
	assert.Contains(t, text, "filming for dispatch 1")

	text, failed = r.call(t, bob, devicetool.VideoStart, `{"title":"Mine"}`)
	assert.True(t, failed)
	assert.Contains(t, text, "filming for dispatch 1")

	text, failed = r.call(t, bob, devicetool.VideoSay, `{"take":"`+take+`","text":"Not mine"}`)
	assert.True(t, failed, text)

	_, failed = r.call(t, alice, devicetool.Screen, `{}`)
	assert.False(t, failed)

	_, failed = r.call(t, alice, devicetool.VideoSay, `{"take":"`+take+`","text":"Tap Sign in","tap":"#3"}`)
	assert.False(t, failed)

	text, failed = r.call(t, alice, devicetool.VideoStop, `{"take":"`+take+`"}`)
	assert.False(t, failed)
	assert.Contains(t, text, "ok")

	_, filming := r.p.Filming()
	assert.False(t, filming)

	_, failed = r.call(t, bob, devicetool.Screen, `{}`)
	assert.False(t, failed)

	for _, call := range r.node.seen() {
		assert.False(t, strings.HasPrefix(call, "d-2 video"), "bob's video calls reached no node: %s", call)
	}

	stored, err := r.takes.Get(take)
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeFinalizing, stored.State)
	assert.Equal(t, "n-phone", stored.Node, "a take is bound to its node")
	assert.Equal(t, "dispatch 1", stored.AgentName)
	require.Len(t, stored.Steps, 1)
}

// Scenario: a start whose answer was lost is called again with its request
// id: the same take. A stop retried, or a stale stop of an old take that
// comes after a new start, answers that take's state and leaves the new one
// filming.
func TestStartAndStopAreRetriedSafely(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	first := r.start(t, alice, "a1")
	again := r.start(t, alice, "a1")
	assert.Equal(t, first, again)

	text, failed := r.call(t, alice, devicetool.VideoStart, `{"title":"Another","request_id":"a2"}`)
	assert.True(t, failed)
	assert.Contains(t, text, "already")

	_, failed = r.call(t, alice, devicetool.VideoStop, `{"take":"`+first+`"}`)
	require.False(t, failed)

	_, failed = r.call(t, alice, devicetool.VideoStop, `{"take":"`+first+`"}`)
	require.False(t, failed, "a stop retried answers the take's state")

	// The start of a take that ended, retried, starts nothing new.
	text, failed = r.call(t, alice, devicetool.VideoStart, `{"title":"Sign in","request_id":"a1"}`)
	assert.False(t, failed)
	assert.Contains(t, text, first)

	_, filming := r.p.Filming()
	assert.False(t, filming)

	second := r.start(t, alice, "a2")
	assert.NotEqual(t, first, second)

	text, failed = r.call(t, alice, devicetool.VideoStop, `{"take":"`+first+`"}`)
	assert.False(t, failed)
	assert.Contains(t, text, first)

	f, filming := r.p.Filming()
	require.True(t, filming, "the stale stop left the new take filming")
	assert.Equal(t, second, f.Take)
}

// Scenario: an agent stops a take it said no line on (issue #1028). The stop, and a stop retried, say the take
// has no steps and cannot be made into a video, and how to film one that can; the take is kept. A take with a
// step is stopped without it.
func TestAStopWithNoStepsSaysTheTakeCannotBeMadeIntoAVideo(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	silent := r.start(t, alice, "a1")

	for _, attempt := range []string{"stop", "stop retried"} {
		text, failed := r.call(t, alice, devicetool.VideoStop, `{"take":"`+silent+`"}`)
		assert.False(t, failed, attempt)
		assert.Contains(t, text, "no steps, so it cannot be made into a video", attempt)
		assert.Contains(t, text, "video_say right before each action", attempt)
	}

	stored, err := r.takes.Get(silent)
	require.NoError(t, err, "the take is kept")
	assert.Empty(t, stored.Steps)

	said := r.start(t, alice, "a2")
	_, failed := r.call(t, alice, devicetool.VideoSay, `{"take":"`+said+`","text":"Tap Sign in"}`)
	require.False(t, failed)

	text, failed := r.call(t, alice, devicetool.VideoStop, `{"take":"`+said+`"}`)
	assert.False(t, failed)
	assert.NotContains(t, text, "no steps")
}

// Scenario: a Bot's pi films in one turn, and the chat's next turn calls the
// phone. The second turn cannot use the first's take: it was ended as the
// first turn's work, and a line said on it is refused.
func TestABotsNextTurnCannotUseTheTakeOfTheTurnBefore(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	turn1 := Caller{Key: "b-cht_1", Name: "bot coordinator in chat cht_1", Work: "turn cht_1 tok@1"}
	turn2 := Caller{Key: "b-cht_1", Name: "bot coordinator in chat cht_1", Work: "turn cht_1 tok@2"}

	take := r.start(t, turn1, "r1")

	text, failed := r.call(t, turn2, devicetool.VideoSay, `{"take":"`+take+`","text":"Still filming?"}`)
	assert.True(t, failed)
	assert.Contains(t, text, "not being filmed")

	stored, err := r.takes.Get(take)
	require.NoError(t, err)
	assert.Equal(t, devicetool.EndedWorkEnd, stored.EndedBy)
}

// Scenario: the work a take was started for ends: a Dispatch reported
// finished or exited, a Bot's turn settled or cut. The phone's sweep ends the
// take as work_end and frees the phone. A phone that stopped running ends it
// as phone_end; a node that went away is told at its next hello.
func TestATakeEndsWithItsWorkOrItsPhone(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	take := r.start(t, alice, "a1")

	r.p.checkLease(t.Context())
	_, filming := r.p.Filming()
	assert.True(t, filming, "the work goes on")

	r.endWork(alice.Work)
	r.p.checkLease(t.Context())

	_, filming = r.p.Filming()
	assert.False(t, filming)

	stored, err := r.takes.Get(take)
	require.NoError(t, err)
	assert.Equal(t, devicetool.EndedWorkEnd, stored.EndedBy)

	second := r.start(t, bob, "b1")
	r.node.said("n-phone", nodeproto.DeviceStopped)
	r.p.checkLease(t.Context())

	stored, err = r.takes.Get(second)
	require.NoError(t, err)
	assert.Equal(t, devicetool.EndedPhoneEnd, stored.EndedBy)

	r.node.said("n-phone", nodeproto.DeviceRunning)
	third := r.start(t, bob, "b2")

	r.node.mu.Lock()
	r.node.online["n-phone"] = false
	r.node.mu.Unlock()

	r.p.checkLease(t.Context())

	_, filming = r.p.Filming()
	assert.False(t, filming, "a node that went away holds no lease")
	assert.Equal(t, devicetool.EndedPhoneEnd, r.p.ending[third], "its end is told at the node's next hello")
}

// Scenario: the owner takes the phone over while a take is filmed. The node
// is asked to pause and answers before the owner's first touch is passed on;
// the view is told the take is paused, and "Hand back" resumes it.
func TestTheOwnersTakeoverPausesTheTakeFirst(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	take := r.start(t, alice, "a1")

	v, err := r.p.Watch(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { v.Close(context.Background()) })

	up := nextUp(t, v)
	assert.Equal(t, ControllerAgent, up.Controller)

	up = nextUp(t, v)
	require.NotNil(t, up.Filming)
	assert.Equal(t, take, up.Filming.Take)
	assert.Equal(t, devicetool.TakeRecording, up.Filming.State)

	require.Eventually(t, func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()

		return v.end != nil
	}, 2*time.Second, 5*time.Millisecond)

	require.NoError(t, v.Do(t.Context(), Action{Type: "takeControl"}))
	require.NoError(t, v.Do(t.Context(), Action{Type: nodeproto.DeviceViewTouch, Action: "down", X: 0.5, Y: 0.5}))

	seen := r.node.seen()
	pause := indexOf(seen, "d-1 video_pause "+take)
	touch := indexOf(seen, "owner touch")
	require.GreaterOrEqual(t, pause, 0, seen)
	require.GreaterOrEqual(t, touch, 0, seen)
	assert.Less(t, pause, touch, "paused before the owner's first touch")

	for {
		up = nextUp(t, v)
		if up.Filming != nil {
			break
		}
	}

	assert.Equal(t, devicetool.TakePaused, up.Filming.State)

	require.NoError(t, v.Do(t.Context(), Action{Type: "releaseControl"}))

	f, ok := r.p.Filming()
	require.True(t, ok)
	assert.Equal(t, devicetool.TakeRecording, f.State)
	assert.Contains(t, r.node.seen(), "d-1 video_resume "+take)

	// A stop while paused ends the take.
	require.NoError(t, v.Do(t.Context(), Action{Type: "takeControl"}))

	_, failed := r.call(t, alice, devicetool.VideoStop, `{"take":"`+take+`"}`)
	assert.False(t, failed)

	stored, err := r.takes.Get(take)
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeFinalizing, stored.State)
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}

	return -1
}

// Scenario (issue #851): the agent reads its take's state while the node is
// still being told of the stop, and after. It is being finished, never
// recording: the stop is recorded as the lease ends, and an answer the node
// gave before the stop, kept after it, does not make the take running again.
func TestATakeIsNeverRecordingAfterItsStop(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	take := r.start(t, alice, "a1")

	r.node.mu.Lock()
	r.node.endHeld, r.node.endArrived = make(chan struct{}), make(chan struct{})
	held, arrived := r.node.endHeld, r.node.endArrived
	r.node.mu.Unlock()

	stopped := make(chan string, 1)

	go func() {
		raw, err := r.p.Call(context.WithoutCancel(t.Context()), alice, devicetool.VideoStop,
			json.RawMessage(`{"take":"`+take+`"}`))
		if err != nil {
			stopped <- err.Error()

			return
		}

		stopped <- string(raw)
	}()

	<-arrived

	text, failed := r.call(t, alice, devicetool.VideoStatus, `{"take":"`+take+`"}`)
	assert.False(t, failed, text)
	assert.Contains(t, text, "is finalizing", "while the node is told of the stop")
	assert.NotContains(t, text, "is recording")

	close(held)
	assert.Contains(t, <-stopped, `"isError":false`)

	// An answer of the node from before the stop, kept late.
	r.p.keep(devicetool.Take{ID: take, State: devicetool.TakeRecording})

	text, _ = r.call(t, alice, devicetool.VideoStatus, `{"take":"`+take+`"}`)
	assert.Contains(t, text, "is finalizing")

	stored, err := r.takes.Get(take)
	require.NoError(t, err)
	assert.Equal(t, devicetool.EndedStop, stored.EndedBy)
}

// Scenario (issue #851): after an interrupted turn an agent lists its takes:
// its own only, the newest first, at most 20, and only those in a state when
// it names one. Another agent's take is never listed, and no file is named.
func TestAnAgentListsItsOwnTakes(t *testing.T) {
	t.Parallel()

	r := newFilmRig(t)
	base := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)

	for i := range 22 {
		require.NoError(t, r.takes.Create(devicetool.Take{
			ID: fmt.Sprintf("tk_%016d", i), Title: fmt.Sprintf("Take %d", i), Agent: alice.Key,
			StartedAt: base.Add(time.Duration(i) * time.Minute), State: devicetool.TakeAvailable,
			EndedBy: devicetool.EndedStop, DurationMs: 65_000,
			Segments: []devicetool.TakeSegment{{File: "seg-001.mp4"}},
		}))
	}

	require.NoError(t, r.takes.Create(devicetool.Take{
		ID: "tk_bob", Title: "Bob's", Agent: bob.Key, StartedAt: base.Add(time.Hour), State: devicetool.TakeAvailable,
	}))

	text, failed := r.call(t, alice, devicetool.VideoList, `{}`)
	require.False(t, failed, text)
	assert.NotContains(t, text, "tk_bob")
	assert.NotContains(t, text, "seg-001")
	assert.Equal(t, devicetool.MaxListed, strings.Count(text, "\n- "))
	assert.Less(t, strings.Index(text, "tk_0000000000000021"), strings.Index(text, "tk_0000000000000020"),
		"the newest first")
	assert.NotContains(t, text, "tk_0000000000000001\"", "only the newest 20")
	assert.Contains(t, text,
		`tk_0000000000000021 "Take 21": available, ended by stop, started 2026-11-18T10:21:00Z, 1:05 long`)

	live := r.start(t, alice, "a1")

	text, failed = r.call(t, alice, devicetool.VideoList, `{"state":"recording"}`)
	require.False(t, failed, text)
	assert.Equal(t, 1, strings.Count(text, "\n- "))
	assert.Contains(t, text, live+` "Sign in": recording`)

	text, failed = r.call(t, bob, devicetool.VideoList, `{"state":"recording"}`)
	require.False(t, failed, text)
	assert.Equal(t, "You have no take that is recording.", text)

	_, err := r.p.Call(t.Context(), alice, devicetool.VideoList, json.RawMessage(`{"state":"lost"}`))
	require.ErrorIs(t, err, devicetool.ErrBadArguments)
}
