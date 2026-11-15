package serviceproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// What the proxy does with what an account cannot do (issue #567): a request
// for an action the account is missing is refused before it is sent (decide),
// and a refusal from the service is a reason to read the account again.

const (
	// recheckEvery is the least time between two reads of one Connection's
	// access after a 403: an agent that tries the same thing ten times reads
	// it once. The length is inference; no reference re-reads on a refusal.
	recheckEvery = time.Minute
	// recheckTimeout bounds one read, which runs after the answer was given.
	recheckTimeout = 2 * time.Minute
	// maxErrorPeek bounds what is read of a Lark refusal to find its code.
	maxErrorPeek = 64 << 10
)

// larkScopeCodes are Lark's codes for a call the app has no scope for, and a
// person's sign-in was not granted one for (lark-cli
// internal/errclass/codemeta.go: SubtypeAppScopeNotApplied,
// SubtypeMissingScope).
var larkScopeCodes = []int{99991672, 99991679}

// cannot is what an agent is told of an action its account cannot do.
func cannot(conn orchestration.Connection, action Action, s session, reason string) string {
	who := conn.Account
	if who == "" {
		who = s.connection()
	}

	return fmt.Sprintf("Refused: %s cannot %q at %s: %s. It was not run; do not try another way. Ask the owner "+
		"to grant it, or to run `hypomnema integrations check %s` once it is granted.", who,
		action.Service+": "+action.Name, s.connection(), reason, s.connection())
}

// noticed looks at the service's answer to a request that ran. Lark's code
// for a missing scope marks the action at once; any other 403 reads the
// account's access again. The answer itself goes back unchanged.
func (p *Proxy) noticed(ctx context.Context, s session, action Action, status int, body []byte) {
	if action.Plumbing || status < http.StatusBadRequest {
		return
	}

	if s.integration == "lark" && p.cfg.Mark != nil {
		var said struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}

		if json.Unmarshal(body, &said) == nil && slices.Contains(larkScopeCodes, said.Code) {
			if _, listed := s.catalog.Action(action.ID); listed {
				reason := fmt.Sprintf("Lark answered code %d: %s", said.Code, clipTitle(said.Msg))
				if err := p.cfg.Mark(context.WithoutCancel(ctx), s.integration, s.label, action.ID,
					missing(reason)); err != nil {
					p.cfg.Logger.ErrorContext(ctx, "an action the account has no scope for could not be recorded",
						slog.String("event", "serviceproxy.access_failed"), slog.String("connection", s.connection()),
						slog.String("action", action.ID), slog.String("error", err.Error()))
				}
			}

			return
		}
	}

	if status == http.StatusForbidden {
		p.recheck(ctx, s)
	}
}

// recheck reads a Connection's access again, at most once a recheckEvery,
// after the answer was given.
func (p *Proxy) recheck(ctx context.Context, s session) {
	if p.cfg.Recheck == nil {
		return
	}

	key, now := s.connection(), time.Now()

	p.mu.Lock()
	if last, ok := p.rechecked[key]; ok && now.Sub(last) < recheckEvery {
		p.mu.Unlock()

		return
	}

	p.rechecked[key] = now
	p.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recheckTimeout)
		defer cancel()

		if err := p.cfg.Recheck(ctx, s.integration, s.label); err != nil {
			p.cfg.Logger.ErrorContext(ctx, "the access of an account could not be read again",
				slog.String("event", "serviceproxy.access_failed"), slog.String("connection", key),
				slog.String("error", err.Error()))
		}
	}()
}
