package modelgateway_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// slowStream answers a stream whose first chunk comes after first, and the rest after rest.
func slowStream(first, rest time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}

		time.Sleep(first)
		_, _ = fmt.Fprint(w, frame(chunkRole))
		flush()
		time.Sleep(rest)
		_, _ = fmt.Fprint(w, frame(chunkText)+frame(chunkFinish)+frame(chunkUsage)+frameDone)
		flush()
	}
}

// Scenario (issue #856): a streamed answer whose first chunk takes 60 ms and whose rest takes 120 ms more. Its
// time to first token is when the first chunk reached the client, not the whole call, and its tokens a second
// count only the time after it. A whole answer has no time to first token, and no tokens a second.
//
// The stream runs in a synctest bubble, on its fake clock, over an in-memory network: the times are exact, and a
// loaded runner that pauses the gateway between two chunks cannot shift them (issue #1018).
func TestTheTimeToFirstTokenIsMeasuredOnAStreamOnly(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		upstream, network := pipeProvider(t, slowStream(60*time.Millisecond, 120*time.Millisecond))
		g := newChatGateway(t, modelgateway.Config{}, upstream)
		g.server.DialProviders(t, network.dial)

		status, _, _ := g.call(t, g.key, `{"model":"gpt","stream":true,"messages":[]}`)
		require.Equal(t, http.StatusOK, status)

		u := g.lastUsage(t)
		require.NotNil(t, u.TTFT)
		assert.Equal(t, 60*time.Millisecond, *u.TTFT, "when the first chunk came")
		assert.Equal(t, 180*time.Millisecond, u.Duration, "when the last chunk came")

		tps, ok := u.TokensPerSecond()
		require.True(t, ok)
		assert.InDelta(t, 7/0.12, tps, 0.001, "7 tokens over the 120 ms after the first chunk")
	})

	plainUpstream, _ := provider(t, plain(http.StatusOK, answer))
	whole := newChatGateway(t, modelgateway.Config{}, plainUpstream)

	status, _, _ := whole.call(t, whole.key, `{"model":"gpt","messages":[]}`)
	require.Equal(t, http.StatusOK, status)

	w := whole.lastUsage(t)
	assert.Nil(t, w.TTFT, "not set equal to the whole call")

	_, ok := w.TokensPerSecond()
	assert.False(t, ok)
}

// pipeProvider is provider on an in-memory network, for a test in a synctest bubble: a read on a loopback socket
// is not durably blocking, so the bubble's clock would never move, while a read on a net.Pipe is (the "I/O"
// section of go.dev/blog/synctest). The gateway reaches it through the network's dial.
func pipeProvider(t *testing.T, handle http.HandlerFunc) (*httptest.Server, *pipeNetwork) {
	t.Helper()

	network := &pipeNetwork{conns: make(chan net.Conn), closed: make(chan struct{})}

	srv := httptest.NewUnstartedServer(providerHandler(&seen{}, handle))
	require.NoError(t, srv.Listener.Close())
	srv.Listener = network
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, network
}

// pipeNetwork is a listener whose every dial is a new net.Pipe.
type pipeNetwork struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (n *pipeNetwork) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	server, client := net.Pipe()

	select {
	case n.conns <- server:
		return client, nil
	case <-n.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (n *pipeNetwork) Accept() (net.Conn, error) {
	select {
	case c := <-n.conns:
		return c, nil
	case <-n.closed:
		return nil, net.ErrClosed
	}
}

func (n *pipeNetwork) Close() error {
	n.once.Do(func() { close(n.closed) })

	return nil
}

func (n *pipeNetwork) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "provider.test" }

// Tokens a second leave out the time to first token, as OmniRoute's generationThroughput.ts, and skip a request
// whose counts were estimated or whose generation took no time.
func TestTokensASecondLeaveOutTheTimeToFirstToken(t *testing.T) {
	t.Parallel()

	ttft := time.Second
	u := modelgateway.Usage{Duration: 3 * time.Second, TTFT: &ttft, Tokens: modelgateway.Tokens{Completion: 100}}

	tps, ok := u.TokensPerSecond()
	require.True(t, ok)
	assert.InDelta(t, 50.0, tps, 0.0001, "100 tokens over the 2 s after the first")

	estimated := u
	estimated.Tokens.Estimated = true
	_, ok = estimated.TokensPerSecond()
	assert.False(t, ok, "estimated counts are not a speed")

	instant := u
	instant.Duration = ttft
	_, ok = instant.TokensPerSecond()
	assert.False(t, ok, "no generation time")

	noStream := u
	noStream.TTFT = nil
	_, ok = noStream.TokensPerSecond()
	assert.False(t, ok, "no first token")
}

// newGatewayDB is a gateway on the fixed clock, with its database.
func newGatewayDB(t *testing.T) (*modelgateway.Service, *sql.DB) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return modelgateway.New(db, fixedClock{}, platform.NewID, nil, nil).WithKeyVault(testKeyVault(t, db)), db
}

// sample records one request to a text model an hour before the fixed clock's now.
func sample(t *testing.T, db *sql.DB, id, key, provider, upstream, account, outcome string, durationMs int64,
	ttftMs *int64, completion int64, estimated bool,
) {
	t.Helper()

	est := 0
	if estimated {
		est = 1
	}

	_, err := db.ExecContext(t.Context(), `INSERT INTO gateway_usage
		(id, key_id, model, provider, upstream_model, stream, attempts, outcome, status, duration_ms, ttft_ms,
		 completion_tokens, estimated, account_id, cost_micros, created_at)
		VALUES (?, ?, 'sol', ?, ?, 1, 1, ?, 200, ?, ?, ?, ?, ?, 10, ?)`,
		id, key, provider, upstream, outcome, durationMs, ttftMs, completion, est, account,
		fixedClock{}.Now().Add(-time.Hour).UnixMilli())
	require.NoError(t, err)
}

// Scenario (issue #856): the owner compares providers on the Usage page. Provider a answered ten requests, from
// 100 ms to 1 s, their first token from 10 to 100 ms, refused one and lost one to a client that left; provider b
// answered one request whose counts were estimated. The comparison has a's p50 and p95 (nearest rank), its
// success rate without the client that left, its median tokens a second, and no speed for b's estimate.
func TestThePerformanceByProviderHasPercentiles(t *testing.T) {
	t.Parallel()

	g, db := newGatewayDB(t)

	k, _, err := g.AddKey(t.Context(), "dongvl", nil, nil)
	require.NoError(t, err)

	for i := int64(1); i <= 10; i++ {
		ttft := i * 10
		sample(t, db, fmt.Sprintf("a%d", i), k.ID, "a", "x", "acc_1", "ok", i*100, &ttft, 90, false)
	}

	sample(t, db, "a-refused", k.ID, "a", "x", "acc_1", "upstream_error", 50, nil, 0, false)
	sample(t, db, "a-gone", k.ID, "a", "x", "acc_1", "client_gone", 50, nil, 0, false)

	ttft := int64(100)
	sample(t, db, "b1", k.ID, "b", "y", "", "ok", 1000, &ttft, 300, true)

	report, err := g.Usage(t.Context(), "24h", 0)
	require.NoError(t, err)
	require.Len(t, report.ByProvider, 2)

	a := report.ByProvider[0]
	assert.Equal(t, "a", a.Provider)
	assert.Equal(t, "x", a.Model)
	assert.True(t, a.Exact)
	assert.Equal(t, int64(12), a.Requests)
	assert.Equal(t, int64(10), a.OK)
	assert.Equal(t, int64(1), a.Failed)
	assert.Equal(t, int64(1), a.ClientGone)

	rate, ok := a.SuccessRate()
	require.True(t, ok)
	assert.InDelta(t, 10.0/11.0, rate, 0.0001)

	assert.Equal(t, 50*time.Millisecond, *a.TTFTP50)
	assert.Equal(t, 100*time.Millisecond, *a.TTFTP95)
	assert.Equal(t, 500*time.Millisecond, *a.LatencyP50)
	assert.Equal(t, time.Second, *a.LatencyP95)
	assert.Equal(t, int64(120), a.CostMicros)
	require.NotNil(t, a.TokensPerSecond)
	// The fifth of the ten rates, slowest first: 90 tokens over 600 - 60 ms.
	assert.InDelta(t, 90/0.54, *a.TokensPerSecond, 0.01)

	b := report.ByProvider[1]
	assert.Equal(t, "b", b.Provider)
	assert.Nil(t, b.TokensPerSecond, "an estimate is not a speed")
	require.NotNil(t, b.TTFTP50)

	require.Len(t, report.SpeedByAccount, 1, "only the requests a subscription account served")
	assert.Equal(t, "acc_1", report.SpeedByAccount[0].AccountID)
	assert.Equal(t, int64(12), report.SpeedByAccount[0].Requests)
}

// Scenario (issue #856): the daily ledger. Two requests are served, a stream and a whole answer; the 90 days read
// from the ledger count what the 30 days read from the requests count, and the speed has averages. Forty days
// later the clean-up deletes the requests; the ledger still holds them.
func TestTheDailyLedgerMatchesTheRequestsAndOutlivesTheCleanUp(t *testing.T) {
	t.Parallel()

	upstream, _ := provider(t, slowStream(20*time.Millisecond, 20*time.Millisecond))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	status, _, _ := g.call(t, g.key, `{"model":"gpt","stream":true,"messages":[]}`)
	require.Equal(t, http.StatusOK, status)

	status, _, _ = g.call(t, g.key, `{"model":"gpt","messages":[]}`)
	require.Equal(t, http.StatusOK, status)

	month, err := g.svc.Usage(t.Context(), "30d", 0)
	require.NoError(t, err)
	quarter, err := g.svc.Usage(t.Context(), "90d", 0)
	require.NoError(t, err)

	assert.False(t, month.Ledger)
	assert.True(t, quarter.Ledger)
	assert.Equal(t, int64(2), quarter.Totals.Requests)
	assert.Equal(t, month.Totals, quarter.Totals)
	assert.Equal(t, month.ByModel, quarter.ByModel)
	require.Len(t, quarter.Buckets, 90)
	assert.Equal(t, int64(2), quarter.Buckets[89].Requests, "today's bar")

	require.Len(t, quarter.ByKey, 1)
	assert.Equal(t, modelgateway.CallerOwner, quarter.ByKey[0].ID, "the ledger names no key")

	require.Len(t, quarter.ByProvider, 1)
	p := quarter.ByProvider[0]
	assert.False(t, p.Exact)
	assert.Nil(t, p.TTFTP50, "no percentiles from sums")
	require.NotNil(t, p.TTFTAvg, "the stream's time to first token")
	require.NotNil(t, p.LatencyAvg)
	assert.Equal(t, month.ByProvider[0].Requests, p.Requests)

	year, err := g.svc.Usage(t.Context(), "12m", 0)
	require.NoError(t, err)
	require.Len(t, year.Buckets, 12)
	assert.Equal(t, int64(2), year.Buckets[11].Requests, "this month's bar")

	// Forty days pass.
	shift := (40 * 24 * time.Hour).Milliseconds()
	_, err = g.db.ExecContext(t.Context(), `UPDATE gateway_usage SET created_at = created_at - ?`, shift)
	require.NoError(t, err)
	_, err = g.db.ExecContext(t.Context(), `UPDATE gateway_usage_daily SET day = day - ?`, shift)
	require.NoError(t, err)

	purged, err := g.svc.Purge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(2), purged.Requests)

	month, err = g.svc.Usage(t.Context(), "30d", 0)
	require.NoError(t, err)
	assert.Zero(t, month.Totals.Requests)

	quarter, err = g.svc.Usage(t.Context(), "90d", 0)
	require.NoError(t, err)
	assert.Equal(t, int64(2), quarter.Totals.Requests, "the ledger survives the clean-up")
	assert.Equal(t, int64(2), quarter.Buckets[49].Requests, "on the day they were served")

	_, _, err = g.svc.UsageRequests(t.Context(), "90d", false, 50, 0)
	require.ErrorIs(t, err, modelgateway.ErrInvalid, "the requests themselves are kept 31 days")
}
