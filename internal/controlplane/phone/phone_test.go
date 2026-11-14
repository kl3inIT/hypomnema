package phone

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// fakeLink stands in for the node API: which nodes are connected, and a
// node's answer to each call, which a test may hold back.
type fakeLink struct {
	mu     sync.Mutex
	online map[string]bool
	calls  []string
	// hold, when set, keeps every call from answering until it is closed.
	hold chan struct{}
	// entered is told when a call reaches the node.
	entered chan string
}

func newLink() *fakeLink {
	return &fakeLink{online: map[string]bool{}, entered: make(chan string, 8)}
}

func (l *fakeLink) Online(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.online[id]
}

func (l *fakeLink) connect(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.online[id] = true
}

func (l *fakeLink) DeviceCall(ctx context.Context, nodeID, agent, tool string, _ json.RawMessage,
) (nodeproto.DeviceAnswer, error) {
	l.mu.Lock()
	l.calls = append(l.calls, nodeID+" "+agent+" "+tool)
	hold := l.hold
	l.mu.Unlock()

	l.entered <- agent

	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nodeproto.DeviceAnswer{}, ctx.Err()
		}
	}

	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: "done for " + agent}}}, nil
}

type fakeNodes struct{ nodes []orchestration.Node }

func (f *fakeNodes) ListNodes(context.Context) ([]orchestration.Node, error) { return f.nodes, nil }

// fakeMachines starts a machine: its node connects after a while, or never.
type fakeMachines struct {
	link    *fakeLink
	started []string
	connect bool
}

func (m *fakeMachines) Start(_ context.Context, nodeID string) (orchestration.Node, error) {
	m.started = append(m.started, nodeID)

	if m.connect {
		go func() {
			time.Sleep(20 * time.Millisecond)
			m.link.connect(nodeID)
		}()
	}

	return orchestration.Node{ID: nodeID}, nil
}

func phoneNode(power orchestration.MachinePower) orchestration.Node {
	return orchestration.Node{
		ID: "n-phone", Name: "phone", Device: nodeproto.DeviceAndroid,
		Machine: &orchestration.Machine{State: orchestration.MachineReady, Power: power},
	}
}

func newPhone(link *fakeLink, machines Machines, nodes ...orchestration.Node) *Phone {
	return New(Config{
		Nodes: &fakeNodes{nodes: nodes}, Link: link, Machines: machines,
		WakeWithin: 300 * time.Millisecond, Poll: 5 * time.Millisecond,
	})
}

// The tools are offered only by a control plane where a node holds a phone.
func TestToolsAreOfferedOnlyWhereANodeHoldsAPhone(t *testing.T) {
	t.Parallel()

	tools, err := newPhone(newLink(), nil, phoneNode("")).Tools(t.Context())
	require.NoError(t, err)
	assert.Len(t, tools, len(devicetool.Tools()))

	_, err = newPhone(newLink(), nil, orchestration.Node{ID: "n-build"}).Tools(t.Context())
	require.ErrorIs(t, err, ErrUnavailable)
}

// The phone has one holder at a time: a second agent's call reaches the node
// only once the first one's has answered.
func TestASecondCallWaitsForTheFirst(t *testing.T) {
	t.Parallel()

	link := newLink()
	link.connect("n-phone")
	link.hold = make(chan struct{})
	p := newPhone(link, nil, phoneNode(""))

	first := make(chan error, 1)
	second := make(chan error, 1)

	go func() {
		_, err := p.Call(t.Context(), "d-1", devicetool.Screen, nil)
		first <- err
	}()

	require.Equal(t, "d-1", <-link.entered)

	go func() {
		_, err := p.Call(t.Context(), "d-2", devicetool.Screen, nil)
		second <- err
	}()

	select {
	case agent := <-link.entered:
		t.Fatalf("%s's call reached the phone while d-1's was running", agent)
	case <-time.After(50 * time.Millisecond):
	}

	close(link.hold)
	require.NoError(t, <-first)
	require.Equal(t, "d-2", <-link.entered)
	require.NoError(t, <-second)
}

// A call that finds the phone's machine stopped starts it and goes on once
// its node said hello.
func TestACallWakesAStoppedMachine(t *testing.T) {
	t.Parallel()

	link := newLink()
	machines := &fakeMachines{link: link, connect: true}
	p := newPhone(link, machines, phoneNode(orchestration.MachineStopped))

	raw, err := p.Call(t.Context(), "d-1", devicetool.Screen, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"n-phone"}, machines.started)

	var answer nodeproto.DeviceAnswer
	require.NoError(t, json.Unmarshal(raw, &answer))
	assert.Equal(t, "done for d-1", answer.Content[0].Text)
}

// A machine whose node does not say hello in time leaves the call with a
// plain word that the phone is waking, and the machine started.
func TestAMachineThatDoesNotWakeInTimeSaysSo(t *testing.T) {
	t.Parallel()

	link := newLink()
	machines := &fakeMachines{link: link}
	p := newPhone(link, machines, phoneNode(orchestration.MachineStopped))

	_, err := p.Call(t.Context(), "d-1", devicetool.Screen, nil)
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Contains(t, err.Error(), "the phone is waking")
	assert.Equal(t, []string{"n-phone"}, machines.started)
	assert.Empty(t, link.calls)
}

// A node with no machine of a provider, or a machine that is on, is not
// started: the call says the node is offline.
func TestAnOfflineNodeThatIsNotStoppedIsNotStarted(t *testing.T) {
	t.Parallel()

	for name, n := range map[string]orchestration.Node{
		"installed by the owner": {ID: "n-phone", Name: "phone", Device: nodeproto.DeviceAndroid},
		"machine on":             phoneNode(""),
	} {
		machines := &fakeMachines{}
		_, err := newPhone(newLink(), machines, n).Call(t.Context(), "d-1", devicetool.Screen, nil)
		require.ErrorIs(t, err, ErrUnavailable, name)
		assert.Contains(t, err.Error(), "offline", name)
		assert.Empty(t, machines.started, name)
	}
}

// A call the phone would refuse is refused before anything waits or wakes.
func TestABadCallIsRefusedAtOnce(t *testing.T) {
	t.Parallel()

	machines := &fakeMachines{}
	p := newPhone(newLink(), machines, phoneNode(orchestration.MachineStopped))

	_, err := p.Call(t.Context(), "d-1", "device_reboot", nil)
	require.ErrorIs(t, err, devicetool.ErrNoSuchTool)

	_, err = p.Call(t.Context(), "d-1", devicetool.Tap, json.RawMessage(`{"target":"the blue one"}`))
	require.ErrorIs(t, err, devicetool.ErrBadArguments)
	assert.Empty(t, machines.started)
	assert.NotErrorIs(t, err, ErrUnavailable)
}
