package serviceproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"time"
)

// authority is the proxy's own certificate authority: it signs a certificate
// for each host a tool asks for, so that the proxy can read the request. Its
// key never leaves the process.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// pem is the authority's certificate, which a tool is told to trust.
	pem []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

const (
	authorityLife = 10 * 365 * 24 * time.Hour
	leafLife      = 7 * 24 * time.Hour
)

func newAuthority() (*authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Hypomnema service proxy"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(authorityLife),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	return &authority{
		cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		leaves: map[string]*tls.Certificate{},
	}, nil
}

// leaf returns a certificate for host, made once and kept while it lasts.
func (a *authority) leaf(host string) (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if kept, ok := a.leaves[host]; ok && time.Now().Before(kept.Leaf.NotAfter.Add(-time.Hour)) {
		return kept, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(leafLife),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, err
	}

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	made := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}
	a.leaves[host] = made

	return made, nil
}
