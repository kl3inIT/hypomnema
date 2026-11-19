package agenttool

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"iter"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

const execRootName = "Hypomnema service proxy (local)"

type execAuthority struct {
	root    *x509.Certificate
	issuers []execIssuer
}

type execIssuer struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

type execCAFile struct {
	Root    []byte           `json:"root"`
	Issuers []execIssuerFile `json:"issuers"`
}

type execIssuerFile struct {
	Cert []byte `json:"cert"`
	Key  []byte `json:"key"`
}

func caTemplate(name string, names []string, now time.Time) *x509.Certificate {
	return &x509.Certificate{
		Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		PermittedDNSDomainsCritical: true, PermittedDNSDomains: names,
		ExcludedIPRanges: []*net.IPNet{
			{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}, {IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
}

func createExecAuthority(names []string) (execCAFile, error) {
	now := time.Now()
	root := caTemplate(execRootName, names, now)
	root.MaxPathLen = 1
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return execCAFile{}, err
	}

	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		return execCAFile{}, err
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		return execCAFile{}, err
	}

	record := execCAFile{Root: rootDER}
	for names := range execIssuerGroups(names) {
		name := fmt.Sprintf("Hypomnema local TLS issuer %d", len(record.Issuers)+1)
		issuer, err := createExecIssuer(root, rootKey, name, names, now)
		if err != nil {
			return execCAFile{}, err
		}

		record.Issuers = append(record.Issuers, issuer)
	}

	return record, nil
}

// execIssuerGroups bounds TLS chain size and keeps an exact hostname's excluded subtree
// from excluding another declared hostname on the same issuer.
func execIssuerGroups(names []string) iter.Seq[[]string] {
	return func(yield func([]string) bool) {
		start := 0
		for index, name := range names {
			if group := names[start:index]; len(group) == 128 || slices.ContainsFunc(group, func(other string) bool {
				return strings.HasSuffix(name, "."+other) || strings.HasSuffix(other, "."+name)
			}) {
				if !yield(group) {
					return
				}
				start = index
			}
		}
		if start < len(names) {
			yield(names[start:])
		}
	}
}

func createExecIssuer(root *x509.Certificate, rootKey *ecdsa.PrivateKey, name string,
	names []string, now time.Time,
) (execIssuerFile, error) {
	issuer := caTemplate(name, names, now)
	issuer.MaxPathLenZero = true
	issuer.ExcludedDNSDomains = excludedSubdomains(names)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return execIssuerFile{}, err
	}

	cert, err := x509.CreateCertificate(rand.Reader, issuer, root, &key.PublicKey, rootKey)
	if err != nil {
		return execIssuerFile{}, err
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	return execIssuerFile{Cert: cert, Key: der}, err
}

func excludedSubdomains(names []string) []string {
	var out []string
	for _, name := range names {
		if !strings.HasPrefix(name, ".") {
			out = append(out, "."+name)
		}
	}

	return out
}

func decodeExecAuthority(raw []byte) (*execAuthority, error) {
	var record execCAFile
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}

	root, err := x509.ParseCertificate(record.Root)
	if err != nil {
		return nil, err
	}

	if err := root.CheckSignatureFrom(root); err != nil {
		return nil, err
	}

	authority := &execAuthority{root: root}
	for _, item := range record.Issuers {
		cert, err := x509.ParseCertificate(item.Cert)
		if err != nil {
			return nil, err
		}

		pk, err := x509.ParsePKCS8PrivateKey(item.Key)
		if err != nil {
			return nil, err
		}

		key, ok := pk.(*ecdsa.PrivateKey)
		if !ok || !key.PublicKey.Equal(cert.PublicKey) {
			return nil, errors.New("local issuer key does not match its certificate")
		}

		if err := cert.CheckSignatureFrom(root); err != nil {
			return nil, err
		}

		authority.issuers = append(authority.issuers, execIssuer{cert: cert, key: key})
	}

	return authority, nil
}

func (a *execAuthority) matches(names []string) bool {
	if !a.root.PermittedDNSDomainsCritical || !slices.Equal(a.root.PermittedDNSDomains, names) ||
		time.Until(a.root.NotAfter) < 24*time.Hour {
		return false
	}

	index := 0
	for group := range execIssuerGroups(names) {
		if index >= len(a.issuers) {
			return false
		}

		cert := a.issuers[index].cert
		if !cert.PermittedDNSDomainsCritical || !slices.Equal(cert.PermittedDNSDomains, group) ||
			!slices.Equal(cert.ExcludedDNSDomains, excludedSubdomains(group)) || !cert.MaxPathLenZero ||
			time.Until(cert.NotAfter) < 24*time.Hour {
			return false
		}

		index++
	}

	return index == len(a.issuers)
}

func (a *execAuthority) rootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.root.Raw})
}

func (a *execAuthority) leaf(host string) (*tls.Certificate, error) {
	if !githost.ValidHost(host) || net.ParseIP(host) != nil {
		return nil, fmt.Errorf("%q is not a DNS service host", host)
	}
	for _, issuer := range a.issuers {
		if slices.ContainsFunc(issuer.cert.PermittedDNSDomains, func(name string) bool {
			if strings.HasPrefix(name, ".") {
				return strings.HasSuffix(host, name)
			}
			return host == name
		}) {
			return issuer.leaf(host)
		}
	}

	return nil, fmt.Errorf("%s is outside the local service proxy CA's allowed hosts", host)
}

func (issuer execIssuer) leaf(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, issuer.cert, &key.PublicKey, issuer.key)
	if err != nil {
		return nil, err
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	return &tls.Certificate{Certificate: [][]byte{der, issuer.cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}
