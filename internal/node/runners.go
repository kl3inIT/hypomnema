package node

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// defaultRuntime is the runtime of an offer that names none: one from before
// a node ran more than Claude Code.
const defaultRuntime = "claude"

// runners gives the daemon one Runner over the runner of each runtime
// (issue #107). A worker is started and adopted by the runner of its offer's
// runtime; what names only a Dispatch goes to every runner, and the ones that
// do not know it do nothing.
type runners map[string]daemon.Runner

var _ daemon.Runner = runners{}

func (rs runners) of(d statedb.Dispatch) (daemon.Runner, error) {
	runtime := d.Offer.Runtime
	if runtime == "" {
		runtime = defaultRuntime
	}

	r, ok := rs[runtime]
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
	for _, r := range rs {
		r.Stop(id)
	}
}

func (rs runners) Release(id string) {
	for _, r := range rs {
		r.Release(id)
	}
}

func (rs runners) Answer(id string, q nodeproto.Question) {
	for _, r := range rs {
		r.Answer(id, q)
	}
}

func (rs runners) Asked(id, reportID, questionID string) {
	for _, r := range rs {
		r.Asked(id, reportID, questionID)
	}
}

func (rs runners) ItemsAcked(ack nodeproto.ItemsAck) {
	for _, r := range rs {
		r.ItemsAcked(ack)
	}
}
