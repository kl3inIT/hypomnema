package modelgateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario: the runs of overflows of keys that are gone (issue #1052, review of PR #1057). A Dispatch's key is
// revoked when it ends and never comes back, so a run is forgotten once overflowCooldown passed since its last
// overflow, as LiteLLM's count of a deployment's failures lives for its cooldown; the map holds only recent runs.
func TestARunOfOverflowsIsForgottenAfterTheCooldown(t *testing.T) {
	t.Parallel()

	o := newOverflows()
	start := time.Unix(1_800_000_000, 0)

	for range allowedOverflows + 1 {
		o.add("gwk_gone", "sol", start)
	}

	o.add("gwk_live", "sol", start.Add(overflowCooldown+time.Second))

	o.mu.Lock()
	defer o.mu.Unlock()
	assert.Len(t, o.runs, 1)
	assert.Contains(t, o.runs, [2]string{"gwk_live", "sol"})
}
