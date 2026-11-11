package node

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// defaultRuntime is the runtime of an offer that names none: one from before
// a node ran more than Claude Code.
const defaultRuntime = "claude"

// worker is what the daemon asks of the runner of one runtime.
type worker interface {
	Start(ctx context.Context, d statedb.Dispatch, r daemon.Reporter) error
	Adopt(ctx context.Context, d statedb.Dispatch, r daemon.Reporter) error
	Stop(id string)
	Release(id string)
	Answer(id string, q nodeproto.Question)
	Asked(id, reportID, questionID string)
}

// runners gives the daemon one Runner over the runner of each runtime
// (issue #107). A worker is started and adopted by the runner of its offer's
// runtime; what names only a Dispatch goes to every runner, and the ones that
// do not know it do nothing. The transcripts of all of them go through one
// forwarder (issue #157), which the acknowledgements of the control plane
// reach.
type runners struct {
	byRuntime   map[string]worker
	transcripts *transcript.Forwarder
}

var _ daemon.Runner = runners{}

func (rs runners) of(d statedb.Dispatch) (worker, error) {
	runtime := d.Offer.Runtime
	if runtime == "" {
		runtime = defaultRuntime
	}

	r, ok := rs.byRuntime[runtime]
	if !ok {
		return nil, fmt.Errorf("this node has no runner for %s", runtime)
	}

	return r, nil
}

func (rs runners) Start(ctx context.Context, d statedb.Dispatch, rep daemon.Reporter) error {
	r, err := rs.of(d)
	if err != nil {
		return err
	}

	return r.Start(ctx, d, rep)
}

func (rs runners) Adopt(ctx context.Context, d statedb.Dispatch, rep daemon.Reporter) error {
	r, err := rs.of(d)
	if err != nil {
		return err
	}

	return r.Adopt(ctx, d, rep)
}

func (rs runners) Stop(id string) {
	for _, r := range rs.byRuntime {
		r.Stop(id)
	}
}

func (rs runners) Release(id string) {
	for _, r := range rs.byRuntime {
		r.Release(id)
	}
}

func (rs runners) Answer(id string, q nodeproto.Question) {
	for _, r := range rs.byRuntime {
		r.Answer(id, q)
	}
}

func (rs runners) Asked(id, reportID, questionID string) {
	for _, r := range rs.byRuntime {
		r.Asked(id, reportID, questionID)
	}
}

func (rs runners) ItemsAcked(ack nodeproto.ItemsAck) {
	if rs.transcripts != nil {
		rs.transcripts.ItemsAcked(ack)
	}
}
