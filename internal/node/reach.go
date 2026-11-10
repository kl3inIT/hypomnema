package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"time"
)

// reachTimeout bounds one try of a git host.
const reachTimeout = 5 * time.Second

// ReachHost reports whether this machine reaches a git host: an HTTPS request
// to it, with no credential, that gets any answer. It goes the way git goes,
// through the proxy the environment names. A host whose certificate this
// machine does not trust still answered: git says why it fails there, which a
// Task that waits for another node would never show.
func ReachHost(ctx context.Context, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, reachTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+host+"/", nil)
	if err != nil {
		return false
	}

	resp, err := reachClient.Do(req)
	if err != nil {
		var (
			unknown  x509.UnknownAuthorityError
			invalid  x509.CertificateInvalidError
			hostname x509.HostnameError
			verify   *tls.CertificateVerificationError
		)

		return errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) ||
			errors.As(err, &verify)
	}

	_ = resp.Body.Close()

	return true
}

// reachClient follows no redirect: the first answer is the host's.
var reachClient = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
