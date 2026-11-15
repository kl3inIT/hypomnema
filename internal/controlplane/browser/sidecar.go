package browser

// The identity's sidecar (issue #540): a DevTools connection of the engine's
// own to the Chrome agent-browser started, so that every page, cross-site
// frame and worker gets the browser's headed identity with its client hints
// before it runs (auto-attach paused at its start), and no page or worker
// ever reads "HeadlessChrome". It sends nothing but the override and the
// release; never Runtime.enable, which sites detect. agent-browser's own
// user-agent setting sends no client hints (cli/src/native/browser.rs).
//
// Adapted from OpenMausBot (server/browser/chrome/sidecar.ts, Apache License
// 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors;
// third_party/openmausbot/). Changed: written in Go, one sidecar for the one
// shared Chrome, and no passkey commands.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// identityLands is how long a start waits for the tabs already open to take
// the identity: a tab that never answers must not keep the browser from
// starting (OpenMausBot's IDENTITY_LANDS_MS).
const identityLands = 3 * time.Second

// sidecarDial bounds connecting the sidecar and its first command.
const sidecarDial = 15 * time.Second

// pausedOnStart attaches to new targets before they run.
var pausedOnStart = map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}

// browserTargets are what the browser's own session attaches to; frames and
// dedicated workers come through their page.
var browserTargets = []map[string]any{
	{"type": "page"}, {"type": "service_worker"}, {"type": "shared_worker"}, {"exclude": true},
}

// override is Emulation.setUserAgentOverride's and Network.setUserAgentOverride's parameters.
type override struct {
	UserAgent         string     `json:"userAgent"`
	UserAgentMetadata uaMetadata `json:"userAgentMetadata"`
	AcceptLanguage    string     `json:"acceptLanguage,omitempty"`
}

// sidecar is the open connection; done is closed when Chrome ends it.
type sidecar struct {
	conn *cdpConn
	stop context.CancelFunc
}

func (s *sidecar) done() <-chan struct{} { return s.conn.closed }

func (s *sidecar) close() {
	s.stop()
	s.conn.close()
}

// startSidecar attaches to the browser at address and gives every target it
// has and will have the identity.
func startSidecar(ctx context.Context, address string, id chromeIdentity, languages string) (*sidecar, error) {
	// What the sidecar sends after its start lives as long as the sidecar.
	life, stop := context.WithCancel(context.WithoutCancel(ctx))

	ctx, cancel := context.WithTimeout(ctx, sidecarDial)
	defer cancel()

	params := override{UserAgent: id.UserAgent, UserAgentMetadata: id.UserAgentMetadata, AcceptLanguage: languages}

	var (
		conn  *cdpConn
		ready = make(chan struct{})
		// first are the targets attached before auto-attach was answered: the ones already open, which
		// Chrome attaches before it answers. Guarded by mu; nil once answered.
		mu    sync.Mutex
		first = []<-chan struct{}{}
	)

	attached := func(ev cdpEvent) {
		if ev.Method != "Target.attachedToTarget" {
			return
		}

		var target struct {
			SessionID  string `json:"sessionId"`
			TargetInfo struct {
				Type string `json:"type"`
			} `json:"targetInfo"`
		}

		if json.Unmarshal(ev.Params, &target) != nil || target.SessionID == "" {
			return
		}

		applied := make(chan struct{})

		mu.Lock()
		if first != nil {
			first = append(first, applied)
		}
		mu.Unlock()

		go func() {
			defer close(applied)

			// The connection is set before the first command that can attach anything is sent.
			<-ready
			applyIdentity(life, conn, target.SessionID, target.TargetInfo.Type, params)
		}()
	}

	c, err := dialCDP(ctx, address, attached)
	if err != nil {
		stop()

		return nil, err
	}

	conn = c
	close(ready)

	filter := map[string]any{"filter": browserTargets}
	for k, v := range pausedOnStart {
		filter[k] = v
	}

	if err := c.send(ctx, "Target.setAutoAttach", filter, "", nil); err != nil {
		c.close()

		stop()

		return nil, err
	}

	// A tab already open is not paused, so it can navigate before its override lands: let those land first.
	mu.Lock()
	open := first
	first = nil
	mu.Unlock()

	deadline := time.NewTimer(identityLands)
	defer deadline.Stop()

	for _, applied := range open {
		select {
		case <-applied:
		case <-deadline.C:
			return &sidecar{conn: c, stop: stop}, nil
		}
	}

	return &sidecar{conn: c, stop: stop}, nil
}

// applyIdentity gives one target the identity and lets it run. A page or a
// frame also attaches to its own frames and workers, paused. A paused worker
// answers its overrides only once it runs, so a worker is sent them and
// released at once.
func applyIdentity(life context.Context, c *cdpConn, session, kind string, params override) {
	ctx, cancel := context.WithTimeout(life, sidecarDial)
	defer cancel()

	switch {
	case kind == "page" || kind == "iframe":
		if c.send(ctx, "Emulation.setUserAgentOverride", params, session, nil) == nil {
			_ = c.send(ctx, "Target.setAutoAttach", pausedOnStart, session, nil)
		}
	case strings.HasSuffix(kind, "worker"):
		if kind != "worker" {
			_, _ = c.start(ctx, "Emulation.setUserAgentOverride", params, session)
		}

		_, _ = c.start(ctx, "Network.setUserAgentOverride", params, session)
	}

	_ = c.send(ctx, "Runtime.runIfWaitingForDebugger", map[string]any{}, session, nil)
}
