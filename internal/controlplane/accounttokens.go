package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// A Claude subscription token the control plane keeps (issue #150): the
// owner pastes the token of `claude setup-token` once, it is a secret of the
// vault, and the node gets it in the offer of each Dispatch, as OpenClaw
// sends a saved subscription credential to its node per session and
// OpenHands looks a secret up as it spawns the CLI
// (docs/research/2026-11-11-claude-credentials-for-workers.md).

// maxAccountTokenBytes bounds what is read as a pasted token.
const maxAccountTokenBytes = 4096

// accountSecretName is the vault's name for an account's token, the same
// form the accounts logged in for the gateway use.
func accountSecretName(accountID string) string {
	return "account-" + strings.ReplaceAll(strings.ToLower(accountID), "_", "-")
}

// accountTokens gives the node API the token of an account the control plane keeps.
type accountTokens struct {
	svc    *orchestration.Service
	vault  *vault.Service
	logger *slog.Logger
}

// Token returns the token of an account that is sent to the node, and false
// for an account a node holds or one logged in for the gateway.
func (a accountTokens) Token(ctx context.Context, accountID string) (string, bool) {
	ref, sent, err := a.svc.AccountTokenRef(ctx, accountID)
	if err != nil || !sent {
		return "", false
	}

	name, ok := vault.NameFromRef(ref)
	if !ok {
		return "", false
	}

	opened, err := a.vault.Open(ctx, name)
	if err != nil {
		// The worker then fails for want of a token, which the owner is told; the reason is here.
		a.logger.ErrorContext(ctx, "reading an account's token",
			slog.String("event", "controlplane.account_token_failed"), slog.String("account_id", accountID),
			slog.String("error", err.Error()))

		return "", false
	}

	return opened.Payload[vault.KeyValue], true
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

	a, err := o.svc.AddTokenAccount(ctx, runtime, label, func(id string) string {
		return vault.Ref(accountSecretName(id))
	})
	if err != nil {
		return err
	}

	_, err = o.vault.Create(ctx, vault.NewSecret{
		Name: accountSecretName(a.ID), Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: token},
		Metadata: map[string]string{"account": a.ID, "runtime": a.Runtime},
	})
	if err != nil {
		// An account that names a secret that is not there would be placed and fail: it is taken back.
		if removeErr := o.svc.RemoveAccountLogin(ctx, a.ID); removeErr != nil {
			return errors.Join(err, removeErr)
		}

		return fmt.Errorf("keeping the token of account %s: %w", a.ID, err)
	}

	o.printf("account %s (%s %s); its token is kept here and sent to the node with each Dispatch\n",
		a.ID, a.Runtime, a.Label)

	return nil
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
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", fmt.Errorf("%w: the token is one line without spaces, as `claude setup-token` prints it", ErrUsage)
	}

	return token, nil
}
