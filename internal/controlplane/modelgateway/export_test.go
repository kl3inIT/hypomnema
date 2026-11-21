package modelgateway

import (
	"context"
	"net"
	"net/http"
	"testing"
)

// GoUsageLimit reads an OpenCode Go refusal as the adapter does, for tests.
var GoUsageLimit = goUsageLimit

// ZenQuotaUsedUp reads an OpenCode Zen refusal as the adapter does, for tests.
var ZenQuotaUsedUp = zenQuotaUsedUp

// DialProviders makes the server reach its providers through dial, such as a test's in-memory network, and drops
// its idle connections when the test ends: a test in a synctest bubble ends only once their goroutines are gone.
func (s *Server) DialProviders(t testing.TB, dial func(ctx context.Context, network, addr string) (net.Conn, error)) {
	t.Helper()

	transport := s.client.Transport.(*http.Transport) //nolint:forcetypeassert // NewServer's own transport
	transport.DialContext = dial
	t.Cleanup(transport.CloseIdleConnections)
}

// TokenWithoutModel asks for a token of a runtime's accounts as a request that names no model would.
func (s *Server) TokenWithoutModel(ctx context.Context, runtime string) error {
	resp, err := s.underAccounts(ctx, runtime, forModel{}, "", nil, func(AccountToken) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if resp != nil {
		_ = resp.Body.Close()
	}

	return err
}
