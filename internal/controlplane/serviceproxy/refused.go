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

// noticed looks at the service's answer to a request that ran. A 403, or
// Lark's code for a missing scope, reads the account's access again; the
// action is then what Assess says of all its methods, not what one refused
// call of it says: an action is several methods, and one of them may need a
// scope the others do not (lark-cli checks a method, not a group of them).
// The answer itself goes back unchanged.
func (p *Proxy) noticed(ctx context.Context, s session, action Action, status int, body []byte) {
	if action.Plumbing || status < http.StatusBadRequest {
		return
	}

	if status == http.StatusForbidden || (s.integration == "lark" && larkScopeRefusal(body)) {
		p.recheck(ctx, s)
	}
}

// larkScopeRefusal says whether a Lark answer is its code for a missing scope.
func larkScopeRefusal(body []byte) bool {
	var said struct {
		Code int `json:"code"`
	}

	return json.Unmarshal(body, &said) == nil && slices.Contains(larkScopeCodes, said.Code)
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
