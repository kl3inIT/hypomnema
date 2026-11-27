package speech

import "net/http"

// SetTransport makes a Service ask its providers through rt: a test answers them in memory, so that a synctest
// bubble's clock moves while the service waits.
func SetTransport(s *Service, rt http.RoundTripper) { s.client.Transport = rt }
