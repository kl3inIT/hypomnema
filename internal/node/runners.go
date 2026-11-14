package node

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/skills"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// entry is one entry of the registry: a runtime's key and what runs it.
type entry struct {
	key string
	rt  runner.Runtime
}

// registry is the Runtime of each key, refused when a key is named twice or
// a known runtime has none (Orca's registry of agents, which throws on a
// duplicate; docs/conventions.md, Strategy behind a registry).
func registry(entries ...entry) (map[string]runner.Runtime, error) {
	out := map[string]runner.Runtime{}

	for _, e := range entries {
		if _, dup := out[e.key]; dup {
			return nil, fmt.Errorf("two runtimes are named %s", e.key)
		}

		out[e.key] = e.rt
	}

	for _, key := range knownRuntimes {
		if out[key] == nil {
			return nil, fmt.Errorf("no runtime is named %s", key)
		}
	}

	return out, nil
}

// runners gives the daemon one Runner over the Supervisor (issue #392). A
// worker is started and adopted with the Runtime of its offer's runtime;
// what names only a Dispatch goes to the Supervisor, which holds its job.
// The transcripts of all of them go through one forwarder (issue #157),
// which the acknowledgements of the control plane reach.
type runners struct {
	supervisor  *runner.Supervisor
	byRuntime   map[string]runner.Runtime
	transcripts *transcript.Forwarder
	// skills fetches the skills an offer names before its worker starts (issue #21).
	skills skills.Cache
}

var _ daemon.Runner = runners{}

func (rs runners) of(d statedb.Dispatch) (runner.Runtime, error) {
	key := d.Offer.Runtime
	if key == "" {
		return nil, fmt.Errorf("the offer of %s names no runtime", d.Offer.DispatchID)
	}

	rt, ok := rs.byRuntime[key]
	if !ok {
		return nil, fmt.Errorf("this node has no runner for %s", key)
	}

	return rt, nil
}

func (rs runners) Start(ctx context.Context, d statedb.Dispatch, rep daemon.Reporter) error {
	rt, err := rs.of(d)
	if err != nil {
		return err
	}

	if err := rs.skills.Ensure(ctx, d.Offer.Skills); err != nil {
		return err
	}

	return rs.supervisor.Start(ctx, rt, d, rep)
}

func (rs runners) Adopt(ctx context.Context, d statedb.Dispatch, rep daemon.Reporter) error {
	rt, err := rs.of(d)
	if err != nil {
		return err
	}

	return rs.supervisor.Adopt(ctx, rt, d, rep)
}

func (rs runners) Stop(id string) { rs.supervisor.Stop(id) }

func (rs runners) Release(id string) { rs.supervisor.Release(id) }

func (rs runners) Answer(id string, q nodeproto.Question) { rs.supervisor.Answer(id, q) }

func (rs runners) Asked(id, reportID, questionID string) {
	rs.supervisor.Asked(id, reportID, questionID)
}

func (rs runners) ItemsAcked(ack nodeproto.ItemsAck) {
	if rs.transcripts != nil {
		rs.transcripts.ItemsAcked(ack)
	}
}
