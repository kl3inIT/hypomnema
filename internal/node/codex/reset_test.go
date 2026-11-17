package codex

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: a worker of the reviewer calls the gateway while every Codex
// account waits for its limit (issue #756). The CLI ends its turn on the
// gateway's usage limit and writes the reset in its local time; the turn ends
// as rate_limited with that reset, so the scheduler waits for it or hands the
// Task to a backup. A failure that names no reset, or one already past, has
// none.
func TestATurnOnTheGatewaysUsageLimitEndsWithItsReset(t *testing.T) {
	t.Parallel()

	hanoi := time.FixedZone("ICT", 7*60*60)
	failedAt := time.Date(2026, 11, 17, 11, 28, 30, 0, time.UTC) // 18:28 in Hanoi

	for name, c := range map[string]struct {
		failure string
		want    *time.Time
	}{
		"today": {
			"You’ve hit your usage limit. Try again at 7:54 PM.",
			at(time.Date(2026, 11, 17, 12, 55, 0, 0, time.UTC)),
		},
		"another day": {
			"You’ve hit your usage limit. Try again at Nov 20th, 2026 9:05 AM.",
			at(time.Date(2026, 11, 20, 2, 6, 0, 0, time.UTC)),
		},
		"after a promotion": {
			"You’ve hit your usage limit. Upgrade to Pro, or try again at 12:10 AM.",
			nil, // today's 00:10 is past
		},
		"midnight hour": {
			"You’ve hit your usage limit. Try again at Nov 18th, 2026 12:10 AM.",
			at(time.Date(2026, 11, 17, 17, 11, 0, 0, time.UTC)),
		},
		"no reset":    {"You’ve hit your usage limit. Try again later.", nil},
		"not a limit": {"Reconnecting... 1/5 (exceeded retry limit, last status: 429 Too Many Requests)", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := resetOf(c.failure, failedAt, hanoi)
			if c.want == nil {
				assert.Nil(t, got)

				return
			}

			require.NotNil(t, got)
			assert.Equal(t, *c.want, *got)
		})
	}

	// The whole turn: the reset reaches the outcome, and survives a daemon restart.
	reset := time.Date(2026, 11, 17, 12, 55, 0, 0, time.UTC)
	before := &turn{failure: "You’ve hit your usage limit. Try again at 7:54 PM.", resets: &reset}
	outcome := (&CodexRuntime{}).Adopted(before.State()).Outcome(nodeproto.Exit{Kind: "exited"})

	assert.Equal(t, "rate_limited", outcome.Class)
	require.NotNil(t, outcome.ResetsAt)
	assert.Equal(t, reset, *outcome.ResetsAt)
}

func at(t time.Time) *time.Time { return &t }
