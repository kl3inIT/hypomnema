//go:build darwin

package agenttool

import (
	"context"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type darwinExecTrust struct{}

func userTrustStore() execTrustStore { return darwinExecTrust{} }

func (darwinExecTrust) trusted(ctx context.Context, a *execAuthority) (bool, error) {
	keychain, err := loginKeychain(ctx)
	if err != nil {
		return false, err
	}
	found, err := loginHasExecRoot(ctx, keychain, execRootHash(a))
	if err != nil || !found {
		return false, err
	}

	name := a.issuers[0].cert.PermittedDNSDomains[0]
	if strings.HasPrefix(name, ".") {
		name = "tls-check" + name
	}
	leaf, err := a.leaf(name)
	if err != nil {
		return false, err
	}
	leafPath, err := writeCACert(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}))
	if err != nil {
		return false, err
	}
	defer os.Remove(leafPath) //nolint:errcheck // Only a public certificate.
	issuerPath, err := writeCACert(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[1]}))
	if err != nil {
		return false, err
	}
	defer os.Remove(issuerPath) //nolint:errcheck // Only a public certificate.
	_, err = runSecurity(ctx, "verify-cert", "-q", "-L", "-k", keychain,
		"-c", leafPath, "-c", issuerPath, "-p", "ssl", "-n", name)
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return false, nil
	}
	return err == nil, err
}

func (darwinExecTrust) install(ctx context.Context, path string) error {
	keychain, err := loginKeychain(ctx)
	if err != nil {
		return err
	}

	_, err = runSecurity(ctx, "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", keychain, path)
	return err
}

func (darwinExecTrust) remove(ctx context.Context, a *execAuthority) error {
	keychain, err := loginKeychain(ctx)
	if err != nil {
		return err
	}
	hash := execRootHash(a)
	found, err := loginHasExecRoot(ctx, keychain, hash)
	if err != nil || !found {
		return err
	}
	_, err = runSecurity(ctx, "delete-certificate", "-t", "-Z", hash, keychain)
	return err
}

func loginHasExecRoot(ctx context.Context, keychain, hash string) (bool, error) {
	output, err := runSecurity(ctx, "find-certificate", "-a", "-Z", "-c", execRootName, keychain)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(output, "\n") {
		if strings.TrimSpace(line) == "SHA-256 hash: "+hash {
			return true, nil
		}
	}

	return false, nil
}

func execRootHash(a *execAuthority) string {
	return fmt.Sprintf("%X", sha256.Sum256(a.root.Raw))
}

func loginKeychain(ctx context.Context) (string, error) {
	output, err := runSecurity(ctx, "login-keychain", "-d", "user")
	if err != nil {
		return "", err
	}
	path, err := strconv.Unquote(strings.TrimSpace(output))
	if err != nil || !filepath.IsAbs(path) {
		return "", errors.New("security did not report a login keychain path")
	}

	return path, nil
}

func runSecurity(ctx context.Context, args ...string) (string, error) {
	//nolint:gosec // The system security tool receives fixed verbs and arguments, never a shell command.
	output, err := exec.CommandContext(ctx, "/usr/bin/security", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("security %s: %w: %s", args[0], err, output)
	}

	return string(output), nil
}
