package orchestration

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1305, row 5): the gateway cools an account down with why it rests, in the closed set, beside the
// provider's own words. The account lists both while it rests; a code outside the set is refused by the table.
func TestACooledAccountKeepsWhyItRestsBesideTheProvidersWords(t *testing.T) {
	t.Parallel()

	s, _, clock := newTestService(t)
	ctx := t.Context()

	account, err := s.AddAccount(ctx, OpenCodeGoRuntime, "go")
	require.NoError(t, err)

	until := clock.Now().Add(2*time.Hour + 15*time.Minute)
	require.NoError(t, s.CoolAccount(ctx, OpenCodeGoRuntime, account.ID, until, "usage_limit",
		"5-hour usage limit reached. Resets in 2hr 15min."))

	accounts, err := s.ListAccounts(ctx)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.NotNil(t, accounts[0].CoolingUntil)
	assert.Equal(t, until.UnixMilli(), accounts[0].CoolingUntil.UnixMilli())
	assert.Equal(t, "usage_limit", accounts[0].CoolingCode)
	assert.Equal(t, "5-hour usage limit reached. Resets in 2hr 15min.", accounts[0].CoolingReason)

	require.Error(t, s.CoolAccount(ctx, OpenCodeGoRuntime, account.ID, until, "slow", "no"),
		"a code outside the closed set")
}

// Scenario: the migration that adds the code. A cooldown a node's Dispatch set after a rate limit is a rate limit;
// one the gateway set before says nothing more than its words. The migration goes down again.
func TestTheCooldownCodeMigrationNamesTheCooldownsBeforeIt(t *testing.T) {
	t.Parallel()

	s, db, clock := newTestService(t)
	ctx := t.Context()

	node, err := s.AddAccount(ctx, "claude", "node")
	require.NoError(t, err)
	gateway, err := s.AddAccount(ctx, OpenCodeGoRuntime, "go")
	require.NoError(t, err)

	p, err := provider(db)
	require.NoError(t, err)

	before := migrationBefore(t, "_account_cooldown_code.sql")
	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)

	until := clock.Now().Add(time.Hour).UnixMilli()
	for id, reason := range map[string]string{node.ID: "rate_limited", gateway.ID: "OpenCode Go refused the key"} {
		_, err = db.ExecContext(ctx,
			"INSERT INTO account_cooldowns (runtime, account_ref, until, reason) "+
				"SELECT runtime, id, ?, ? FROM accounts WHERE id = ?", until, reason, id)
		require.NoError(t, err)
	}

	require.NoError(t, Migrate(ctx, db))

	accounts, err := s.ListAccounts(ctx)
	require.NoError(t, err)

	codes := map[string]string{}
	for _, a := range accounts {
		codes[a.ID] = a.CoolingCode
	}

	assert.Equal(t, map[string]string{node.ID: "rate_limit", gateway.ID: "unknown"}, codes)

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err, "the migration goes down")
}

// migrationBefore returns the version of the migration before the one whose file name ends with suffix, which `make
// migration-fix` renumbers before the merge.
func migrationBefore(t *testing.T, suffix string) int64 {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	var before int64

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), suffix) {
			return before
		}

		before = version
	}

	require.FailNow(t, "no migration "+suffix)

	return 0
}
