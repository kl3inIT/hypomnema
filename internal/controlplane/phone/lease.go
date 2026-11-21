package phone

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// An agent filming the phone (issue #817, rows 6 to 9). A take holds the
// phone for the agent that started it: a lease of {take, agent key, work},
// where work is the worker's Dispatch or the Bot's turn. While it lives, only
// that agent's phone calls go through, the per-call semaphore still under
// them, as the phone's one holder is OpenMausBot's turn that holds its phone
// (server/drivers/phone-proxy.ts); another agent is told the phone is
// filming (the owner's ask that one agent films without others interfering,
// 2026-11-18). The lease ends at video_stop, when the work ends (a Dispatch
// reported finished or exited, a Bot's turn settled or cut), when the phone
// or its node ends, and, for a control plane that restarts, at the node's
// next hello (nodeapi). The owner's takeover pauses the take on the node and
// waits for its answer before the owner can touch (view.go).

// Caller is an agent calling a phone tool: Key names it to the node (its
// screens, its takes), Name to people, and Work is what it works on now, a
// Dispatch or a Bot's turn, whose end ends its take.
type Caller struct {
	Key  string
	Name string
	Work string
}

// TakeStore keeps the takes on the control plane (package takes).
type TakeStore interface {
	// Create records a take as it starts, bound to its node.
	Create(t devicetool.Take) error
	// Get reads a take; ErrNoTake when there is none.
	Get(id string) (devicetool.Take, error)
	// Update keeps what the node said of a running take.
	Update(t devicetool.Take) (devicetool.Take, error)
	// ByRequest finds an agent's take by the request id it started it with.
	ByRequest(agent, requestID string) (devicetool.Take, bool)
	// Deleted says whether the owner deleted a take.
	Deleted(id string) bool
	// List lists the takes not deleted, the newest first.
	List() []devicetool.Take
}

// ErrNoTake reports a take the control plane does not know.
var ErrNoTake = errors.New("no such take")

// lease is the take that holds the phone.
type lease struct {
	take      string
	title     string
	caller    Caller
	requestID string
	node      string
	// state is the take's state as the node said it last; atMs its time then, at when the node said it; said the
	// last line said.
	state string
	atMs  int64
	at    time.Time
	said  string
}

// Filming is the take that holds the phone, for the owner's view.
type Filming struct {
	Take  string `json:"take"`
	Title string `json:"title"`
	Agent string `json:"agent"`
	State string `json:"state"`
	// AtMs is the take's time at At.
	AtMs int64     `json:"at_ms"`
	At   time.Time `json:"at"`
	Said string    `json:"said,omitempty"`
}

// Filming is the take that holds the phone now, if any.
func (p *Phone) Filming() (Filming, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	l := p.lease
	if l == nil {
		return Filming{}, false
	}

	return Filming{
		Take: l.take, Title: l.title, Agent: l.caller.Name, State: l.state, AtMs: l.atMs, At: l.at, Said: l.said,
	}, true
}

// filmed tells the views that the take holding the phone changed.
func (p *Phone) filmed() {
	p.mu.Lock()
	defer p.mu.Unlock()

	close(p.filmChanged)
	p.filmChanged = make(chan struct{})
}

// filmingChanged is closed when the take holding the phone changes.
func (p *Phone) filmingChanged() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.filmChanged
}

// leasedTo is why a caller may not use the phone now: another agent's take
// holds it. Empty when it may.
func (p *Phone) leasedTo(c Caller) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.lease == nil || p.lease.caller.Key == c.Key {
		return ""
	}

	return "The phone is filming for " + p.lease.caller.Name + ": no other agent can use it until that take " +
		"ends. Wait and try again later; do not try another way to reach the phone."
}

// endStale ends the caller's own take when the work that started it has
// ended: a Bot's next turn does not use the take of its turn before.
func (p *Phone) endStale(ctx context.Context, c Caller) {
	p.mu.Lock()
	l := p.lease
	p.mu.Unlock()

	if l != nil && l.caller.Key == c.Key && l.caller.Work != c.Work {
		p.endLease(ctx, l.take, devicetool.EndedWorkEnd)
	}
}

// video carries out a video tool for the caller.
func (p *Phone) video(ctx context.Context, c Caller, call devicetool.Call) (nodeproto.DeviceAnswer, error) {
	switch call.Tool {
	case devicetool.VideoStart:
		return p.startTake(ctx, c, call)
	case devicetool.VideoSay:
		return p.sayTake(ctx, c, call)
	case devicetool.VideoStop:
		return p.stopTake(ctx, c, call.Take)
	case devicetool.VideoList:
		return p.listTakes(c, call.State), nil
	default:
		return p.takeStatus(ctx, c, call.Take)
	}
}

// startTake starts a take for the caller, holding the phone for it. A start
// retried with its request id answers the same take.
func (p *Phone) startTake(ctx context.Context, c Caller, call devicetool.Call) (nodeproto.DeviceAnswer, error) {
	if p.cfg.Takes == nil {
		return failure("This control plane does not keep takes."), nil
	}

	if call.RequestID != "" {
		if t, ok := p.cfg.Takes.ByRequest(c.Key, call.RequestID); ok && !t.Running() {
			return takeAnswer(t, "This request id started take "+t.ID+" already: "), nil
		}
	}

	if !p.hold.Enter() {
		return failure(Refusal), nil
	}
	defer p.hold.Leave()

	n, err := p.node(ctx)
	if err != nil {
		return nodeproto.DeviceAnswer{}, err
	}

	p.mu.Lock()

	l := p.lease

	switch {
	case l != nil && l.caller.Key != c.Key:
		p.mu.Unlock()

		return failure(p.leasedTo(c)), nil
	case l != nil && (call.RequestID == "" || l.requestID != call.RequestID):
		p.mu.Unlock()

		return failure("You are filming take " + l.take + " already: stop it with video_stop first."), nil
	case l == nil:
		l = &lease{
			take: newTakeID(), title: call.Title, caller: c, requestID: call.RequestID, node: n.ID,
			state: devicetool.TakeRecording, at: p.cfg.Clock.Now(),
		}
		p.lease = l
	}
	p.mu.Unlock()

	if _, err := p.cfg.Takes.Get(l.take); errors.Is(err, ErrNoTake) {
		err = p.cfg.Takes.Create(devicetool.Take{
			ID: l.take, Title: call.Title, Agent: c.Key, AgentName: c.Name, Node: n.ID, RequestID: call.RequestID,
			StartedAt: p.cfg.Clock.Now().UTC(), State: devicetool.TakeRecording,
		})
		if err != nil {
			p.dropLease(l.take)

			return nodeproto.DeviceAnswer{}, fmt.Errorf("recording the take: %w", err)
		}
	}

	args, _ := json.Marshal(map[string]string{ //nolint:errchkjson // Strings.
		"title": call.Title, "request_id": call.RequestID, "take": l.take,
	})

	answer, err := p.toNode(ctx, c.Key, devicetool.VideoStart, args)
	if err != nil || answer.IsError || answer.Take == nil {
		p.dropLease(l.take)

		why := "the phone's node did not start it"
		if err != nil {
			why = err.Error()
		} else if text := answerText(answer); text != "" {
			why = text
		}

		_, _ = p.cfg.Takes.Update(devicetool.Take{
			ID: l.take, State: devicetool.TakeFailed, EndedBy: devicetool.EndedStreamError, Reason: why,
		})

		if err != nil {
			return nodeproto.DeviceAnswer{}, err
		}

		return answer, nil
	}

	p.keep(*answer.Take)

	p.cfg.Logger.InfoContext(ctx, "an agent started filming the phone", slog.String("event", "phone.take_started"),
		slog.String("take", l.take), slog.String("agent", c.Name))

	return answer, nil
}

// sayTake stamps a line on the caller's running take.
func (p *Phone) sayTake(ctx context.Context, c Caller, call devicetool.Call) (nodeproto.DeviceAnswer, error) {
	if why := p.ownTake(c, call.Take); why != "" {
		return failure(why), nil
	}

	args := map[string]string{"take": call.Take, "text": call.Text}
	if call.Chapter != "" {
		args["chapter"] = call.Chapter
	}

	if call.Target != nil {
		args["tap"] = pointText(*call.Target)
	}

	raw, _ := json.Marshal(args) //nolint:errchkjson // Strings.

	answer, err := p.toNode(ctx, c.Key, devicetool.VideoSay, raw)
	if err != nil {
		return nodeproto.DeviceAnswer{}, err
	}

	if answer.Take != nil {
		p.mu.Lock()
		if p.lease != nil && p.lease.take == call.Take {
			p.lease.said = call.Text
		}
		p.mu.Unlock()

		p.keep(*answer.Take)
	}

	return answer, nil
}

// stopTake stops the caller's take. A take that ended already, the one before
// a new start included, answers its state and leaves the running one alone.
func (p *Phone) stopTake(ctx context.Context, c Caller, id string) (nodeproto.DeviceAnswer, error) {
	p.mu.Lock()
	l := p.lease
	running := l != nil && l.take == id && l.caller.Key == c.Key
	p.mu.Unlock()

	if !running {
		answer, err := p.takeStatus(ctx, c, id)

		return withoutSteps(answer), err
	}

	answer := p.endLease(ctx, id, devicetool.EndedStop)
	if answer.Take == nil {
		answer, err := p.takeStatus(ctx, c, id)

		return withoutSteps(answer), err
	}

	return withoutSteps(answer), nil
}

// noSteps is what video_stop adds on a take with no step (issue #1028): a video's scenes are its steps, and Make
// refuses a take without them (videos/compose/scenes.go, ErrNoSteps), as autodemo refuses a script with no scenes
// (src/demo.js:11). The take is kept.
const noSteps = "It has no steps, so it cannot be made into a video; it is kept. To make a video, film it again " +
	"with video_say right before each action."

// withoutSteps adds noSteps to video_stop's answer when the take it stopped has no step.
func withoutSteps(a nodeproto.DeviceAnswer) nodeproto.DeviceAnswer {
	if a.IsError || a.Take == nil || a.Take.Running() || len(a.Take.Steps) > 0 {
		return a
	}

	a.Content = append(slices.Clone(a.Content), nodeproto.DeviceContent{Type: "text", Text: noSteps})

	return a
}

// takeStatus answers a take's state: the node's while it is filmed, the
// control plane's after.
func (p *Phone) takeStatus(ctx context.Context, c Caller, id string) (nodeproto.DeviceAnswer, error) {
	if p.cfg.Takes == nil {
		return failure("This control plane does not keep takes."), nil
	}

	t, err := p.cfg.Takes.Get(id)
	if err != nil || t.Agent != c.Key {
		return failure("You have no take " + id + "."), nil //nolint:nilerr // Answered to the agent.
	}

	p.mu.Lock()
	running := p.lease != nil && p.lease.take == id
	p.mu.Unlock()

	if running {
		raw, _ := json.Marshal(map[string]string{"take": id}) //nolint:errchkjson // Strings.

		answer, err := p.toNode(ctx, c.Key, devicetool.VideoStatus, raw)
		if err == nil && answer.Take != nil {
			p.keep(*answer.Take)

			return answer, nil
		}
	}

	return takeAnswer(t, ""), nil
}

// listTakes lists the caller's own takes, the newest first, at most
// devicetool.MaxListed, in a state when one is named (issue #851): the
// owner's `GET /takes` restricted to the agent, as video_status is.
func (p *Phone) listTakes(c Caller, state string) nodeproto.DeviceAnswer {
	if p.cfg.Takes == nil {
		return failure("This control plane does not keep takes.")
	}

	var own []devicetool.Take

	for _, t := range p.cfg.Takes.List() {
		if t.Agent != c.Key {
			continue
		}

		if state != "" && t.State != state {
			continue
		}

		own = append(own, t)
		if len(own) == devicetool.MaxListed {
			break
		}
	}

	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: devicetool.List(own, state)}}}
}

// ownTake says why the caller may not say a line on a take: it is not its
// running take. Empty when it may.
func (p *Phone) ownTake(c Caller, id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch {
	case p.lease != nil && p.lease.caller.Key != c.Key:
		return "The phone is filming for " + p.lease.caller.Name + ", not you."
	case p.lease == nil || p.lease.take != id:
		return "Take " + id + " is not being filmed: start one with video_start."
	}

	return ""
}

// endLease ends the take holding the phone, for endedBy: the node is told,
// and answers the take's state, which is kept.
func (p *Phone) endLease(ctx context.Context, id, endedBy string) nodeproto.DeviceAnswer {
	l := p.dropLease(id)
	if l == nil {
		return nodeproto.DeviceAnswer{}
	}

	// From here the take is no longer filmed for its agent: a video_status read while the node is told says it
	// is being finished, never recording (issue #851, after #817's rows 9 and 10).
	if p.cfg.Takes != nil {
		if _, err := p.cfg.Takes.Update(devicetool.Take{
			ID: id, State: devicetool.TakeFinalizing, EndedBy: endedBy,
		}); err != nil {
			p.cfg.Logger.WarnContext(ctx, "a take's end could not be recorded",
				slog.String("event", "phone.take_record_failed"), slog.String("take", id),
				slog.String("error", err.Error()))
		}
	}

	raw, _ := json.Marshal(map[string]string{"take": id, "ended_by": endedBy}) //nolint:errchkjson // Strings.

	answer, err := p.toNode(context.WithoutCancel(ctx), l.caller.Key, devicetool.VideoEnd, raw)
	if err != nil || answer.Take == nil {
		// The node is told again at its next hello, which names the take it still films.
		p.mu.Lock()
		p.ending[id] = endedBy
		p.mu.Unlock()

		p.cfg.Logger.WarnContext(ctx, "the end of a take could not reach the phone's node",
			slog.String("event", "phone.take_end_failed"), slog.String("take", id))

		return nodeproto.DeviceAnswer{}
	}

	p.keep(*answer.Take)

	p.cfg.Logger.InfoContext(ctx, "a take ended", slog.String("event", "phone.take_ended"),
		slog.String("take", id), slog.String("ended_by", endedBy))

	return answer
}

// dropLease ends the lease of a take, if it holds the phone, and returns it.
func (p *Phone) dropLease(id string) *lease {
	p.mu.Lock()

	l := p.lease
	if l == nil || l.take != id {
		p.mu.Unlock()

		return nil
	}

	p.lease = nil
	p.mu.Unlock()

	p.filmed()

	return l
}

// keep records what the node said of a take: in the store, and on the lease
// while it holds the phone.
func (p *Phone) keep(t devicetool.Take) {
	if p.cfg.Takes != nil {
		if _, err := p.cfg.Takes.Update(t); err != nil {
			p.cfg.Logger.Warn("a take could not be recorded", slog.String("event", "phone.take_record_failed"),
				slog.String("take", t.ID), slog.String("error", err.Error()))
		}
	}

	p.mu.Lock()
	if p.lease != nil && p.lease.take == t.ID {
		p.lease.state, p.lease.atMs, p.lease.at = t.State, t.DurationMs, p.cfg.Clock.Now()
	}
	p.mu.Unlock()

	p.filmed()
}

// checkLease ends the take holding the phone when its work ended, or its
// phone or its node did.
func (p *Phone) checkLease(ctx context.Context) {
	p.mu.Lock()
	l := p.lease
	p.mu.Unlock()

	if l == nil {
		return
	}

	switch {
	case !p.cfg.Link.Online(l.node):
		// The node films on; it is told to end the take at its next hello.
		if p.dropLease(l.take) != nil {
			p.mu.Lock()
			p.ending[l.take] = devicetool.EndedPhoneEnd
			p.mu.Unlock()
		}
	case p.phoneEnded(l.node):
		p.endLease(ctx, l.take, devicetool.EndedPhoneEnd)
	case p.cfg.WorkAlive != nil && !p.cfg.WorkAlive(ctx, l.caller.Work):
		p.endLease(ctx, l.take, devicetool.EndedWorkEnd)
	}
}

// phoneEnded says whether the node said its phone is not running.
func (p *Phone) phoneEnded(nodeID string) bool {
	st, ok := p.cfg.Link.DeviceState(nodeID)

	return ok && st.State != nodeproto.DeviceRunning
}

// pauseFilming pauses the take holding the phone for the owner's takeover,
// and returns once the node said it paused: nothing the owner does after is
// filmed. A node that cannot be reached films nothing the owner does either.
func (p *Phone) pauseFilming(ctx context.Context) {
	p.mu.Lock()
	l := p.lease
	p.mu.Unlock()

	if l == nil || l.state != devicetool.TakeRecording {
		return
	}

	raw, _ := json.Marshal(map[string]string{"take": l.take}) //nolint:errchkjson // Strings.

	answer, err := p.toNode(ctx, l.caller.Key, devicetool.VideoPause, raw)
	if err != nil || answer.Take == nil {
		p.cfg.Logger.WarnContext(ctx, "a take could not be paused for the owner",
			slog.String("event", "phone.take_pause_failed"), slog.String("take", l.take))

		return
	}

	p.keep(*answer.Take)
}

// resumeFilming resumes the take the owner's takeover paused.
func (p *Phone) resumeFilming(ctx context.Context) {
	p.mu.Lock()
	l := p.lease
	p.mu.Unlock()

	if l == nil || l.state != devicetool.TakePaused {
		return
	}

	raw, _ := json.Marshal(map[string]string{"take": l.take}) //nolint:errchkjson // Strings.

	answer, err := p.toNode(context.WithoutCancel(ctx), l.caller.Key, devicetool.VideoResume, raw)
	if err != nil || answer.Take == nil {
		p.cfg.Logger.WarnContext(ctx, "a take could not be resumed", slog.String("event", "phone.take_resume_failed"),
			slog.String("take", l.take))

		return
	}

	p.keep(*answer.Take)
}

// NodeTakes settles the takes a node says it holds at its hello: one it
// films that no lease holds any more is to be ended, for why its end did not
// reach the node, or "restart" when the control plane restarted since it
// started; one the owner deleted is to be dropped.
func (p *Phone) NodeTakes(ctx context.Context, nodeID string, held []nodeproto.TakeHeld,
) ([]nodeproto.TakeEnd, []string) {
	var (
		end  []nodeproto.TakeEnd
		drop []string
	)

	for _, h := range held {
		if p.cfg.Takes != nil && p.cfg.Takes.Deleted(h.ID) {
			drop = append(drop, h.ID)

			continue
		}

		if h.State != devicetool.TakeRecording && h.State != devicetool.TakePaused {
			continue
		}

		p.mu.Lock()
		leased := p.lease != nil && p.lease.take == h.ID && p.lease.node == nodeID
		why, told := p.ending[h.ID]
		delete(p.ending, h.ID)
		p.mu.Unlock()

		if leased {
			continue
		}

		if !told {
			why = devicetool.EndedRestart
		}

		end = append(end, nodeproto.TakeEnd{ID: h.ID, EndedBy: why})
		p.cfg.Logger.InfoContext(ctx, "a take its node still films is ended", slog.String("event", "phone.take_orphaned"),
			slog.String("take", h.ID), slog.String("ended_by", why))
	}

	return end, drop
}

// EndTake ends a take that holds the phone, as stopped: the owner deletes it.
func (p *Phone) EndTake(ctx context.Context, id string) {
	p.endLease(ctx, id, devicetool.EndedStop)
}

// toNode runs a video tool on the phone's node, waking it first.
func (p *Phone) toNode(ctx context.Context, agent, tool string, args json.RawMessage) (nodeproto.DeviceAnswer, error) {
	n, err := p.node(ctx)
	if err != nil {
		return nodeproto.DeviceAnswer{}, err
	}

	if !p.cfg.Link.Online(n.ID) {
		if err := p.wake(ctx, n); err != nil {
			return nodeproto.DeviceAnswer{}, err
		}
	}

	answer, err := p.cfg.Link.DeviceCall(ctx, n.ID, agent, tool, args)
	if err != nil {
		return nodeproto.DeviceAnswer{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	return answer, nil
}

// newTakeID is a new take's id.
func newTakeID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)

	return "tk_" + hex.EncodeToString(raw)
}

// pointText is a point as a tool takes it.
func pointText(pt devicetool.Point) string {
	if pt.Box > 0 {
		return fmt.Sprintf("#%d", pt.Box)
	}

	return fmt.Sprintf("%d,%d", pt.X, pt.Y)
}

// answerText is what an answer says.
func answerText(a nodeproto.DeviceAnswer) string {
	var parts []string

	for _, c := range a.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}

	return strings.Join(parts, " ")
}

// failure is a tool's result that failed with why.
func failure(why string) nodeproto.DeviceAnswer {
	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: why}}, IsError: true}
}

// takeAnswer is a take's state as a tool's result, after lead.
func takeAnswer(t devicetool.Take, lead string) nodeproto.DeviceAnswer {
	return nodeproto.DeviceAnswer{
		Content: []nodeproto.DeviceContent{{Type: "text", Text: lead + devicetool.Describe(t)}}, Take: &t,
	}
}
