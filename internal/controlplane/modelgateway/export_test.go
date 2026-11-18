package modelgateway

import (
	"context"
	"net/http"
)

// GoUsageLimit reads an OpenCode Go refusal as the adapter does, for tests.
var GoUsageLimit = goUsageLimit

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
