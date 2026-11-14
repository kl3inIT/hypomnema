package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// A Claude subscription token the control plane keeps (issue #150): the
// owner pastes the token of `claude setup-token` once and it is a secret of
// the vault, as OpenClaw's `models auth paste-token` keeps one. The model
// gateway serves the account with it; it never leaves the control plane, and
// a Claude Code worker gets only its Dispatch's gateway key (issue #381,
// docs/decisions/2026-11-14-claude-code-workers-only-through-the-gateway.md).

// maxAccountTokenBytes bounds what is read as a pasted token.
const maxAccountTokenBytes = 4096

// accountSecretName is the vault's name for an account's token, the same
// form the accounts logged in for the gateway use.
func accountSecretName(accountID string) string {
	return "account-" + strings.ReplaceAll(strings.ToLower(accountID), "_", "-")
}

// accountTokens keeps the accounts whose token the owner pastes.
type accountTokens struct {
	svc   *orchestration.Service
	vault *vault.Service
}

// accountsAddToken is `accounts add <runtime> <label> --token FILE|-`: an
// account whose token the control plane keeps. The token is read from a file
// or standard input, never an argument, and is not printed.
func (o *owner) accountsAddToken(ctx context.Context, runtime, label, file string) error {
	if !o.vaultReady {
		return fmt.Errorf("%w: the vault has no key; set it up before keeping a token (secrets keygen)", ErrUsage)
	}

	token, err := o.readToken(file)
	if err != nil {
		return err
	}

	a, err := addTokenAccount(ctx, o.svc, o.vault, runtime, label, token)
	if err != nil {
		return err
	}

	o.printf("account %s (%s %s); its token is kept here and the model gateway serves it\n",
		a.ID, a.Runtime, a.Label)

	return nil
}

// Add keeps an account whose token the owner pasted on the Models page
// (issue #199): the account `accounts add --token` makes, as OpenClaw's
// `models auth paste-token` and 9router's "Paste token" keep one.
func (a accountTokens) Add(ctx context.Context, runtime, label, token string) (string, error) {
	token = strings.TrimSpace(token)
	if !tokenForm(token) {
		return "", fmt.Errorf("%w: the token is one line without spaces, as `claude setup-token` prints it",
			orchestration.ErrInvalid)
	}

	added, err := addTokenAccount(ctx, a.svc, a.vault, runtime, label, token)
	if err != nil {
		return "", err
	}

	return added.ID, nil
}

// addTokenAccount makes the account and seals its token in the vault.
func addTokenAccount(ctx context.Context, svc *orchestration.Service, v *vault.Service,
	runtime, label, token string,
) (orchestration.Account, error) {
	a, err := svc.AddTokenAccount(ctx, runtime, label, func(id string) string {
		return vault.Ref(accountSecretName(id))
	})
	if err != nil {
		return orchestration.Account{}, err
	}

	_, err = v.Create(ctx, vault.NewSecret{
		Name: accountSecretName(a.ID), Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: token},
		Metadata: map[string]string{"account": a.ID, "runtime": a.Runtime},
	})
	if err != nil {
		// An account that names a secret that is not there would be placed and fail: it is taken back.
		if removeErr := svc.RemoveAccountLogin(ctx, a.ID); removeErr != nil {
			return orchestration.Account{}, errors.Join(err, removeErr)
		}

		return orchestration.Account{}, fmt.Errorf("keeping the token of account %s: %w", a.ID, err)
	}

	return a, nil
}

// tokenForm says a token is one line without spaces, as `claude setup-token` prints it.
func tokenForm(token string) bool {
	return token != "" && len(token) <= maxAccountTokenBytes && !strings.ContainsAny(token, " \t\r\n")
}

// readToken reads a token from a file or, for "-", standard input: its first line.
func (o *owner) readToken(file string) (string, error) {
	in := o.in

	if file != "-" {
		f, err := os.Open(file) //nolint:gosec // the owner names the file on the server
		if err != nil {
			return "", fmt.Errorf("opening the token's file: %w", err)
		}
		defer f.Close() //nolint:errcheck // read only

		in = f
	}

	raw, err := io.ReadAll(io.LimitReader(in, maxAccountTokenBytes))
	if err != nil {
		return "", fmt.Errorf("reading the token: %w", err)
	}

	line, _, _ := strings.Cut(string(raw), "\n")

	token := strings.TrimSpace(line)
	if !tokenForm(token) {
		return "", fmt.Errorf("%w: the token is one line without spaces, as `claude setup-token` prints it", ErrUsage)
	}

	return token, nil
}
