package controlplane

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The owner's commands for accounts. An account is added by hand when its
// CLI was logged in on a node, or logged in here (issue #20, step 2): then
// its tokens are in the vault and the control plane refreshes them.

const accountsUsage = "accounts add <runtime> <label> | accounts login codex [--label L] | accounts list | " +
	"accounts refresh <account> | accounts rm <account>"

// maxPastedBytes bounds the address the owner pastes to finish a login.
const maxPastedBytes = 16 * 1024

// secretReferrers names what still uses a secret: the providers and the
// accounts that name it.
func secretReferrers(svc *orchestration.Service) vault.Referrers {
	return func(ctx context.Context, ref string) ([]string, error) {
		providers, err := svc.ProvidersUsingKeyRef(ctx, ref)
		if err != nil {
			return nil, err
		}

		accounts, err := svc.AccountsUsingSecretRef(ctx, ref)
		if err != nil {
			return nil, err
		}

		return append(providers, accounts...), nil
	}
}

// newSubscriptions returns the service that logs subscription accounts in and
// refreshes them, with every vendor there is.
func newSubscriptions(
	cfg Config, svc *orchestration.Service, secrets *vault.Service, logger *slog.Logger,
) *subscription.Service {
	clock := platform.SystemClock{}

	return subscription.New(svc, secrets, clock, logger, subscription.NewCodex(cfg.CodexIssuer, clock))
}

func (o *owner) accounts(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%w: %s", ErrUsage, accountsUsage)

	switch {
	case len(args) == 3 && args[0] == "add":
		//nolint:gosec // G602 false positive: this case holds only when len(args) == 3
		a, err := o.svc.AddAccount(ctx, args[1], args[2])
		if err != nil {
			return err
		}

		o.printf("account %s (%s %s)\n", a.ID, a.Runtime, a.Label)

		return nil
	case len(args) >= 2 && args[0] == "login":
		return o.accountsLogin(ctx, args[1], args[2:])
	case len(args) == 1 && args[0] == "list":
		return o.accountsList(ctx)
	case len(args) == 2 && args[0] == "refresh":
		a, err := o.subscriptions.Refresh(ctx, args[1])
		if err != nil {
			return err
		}

		o.printf("account %s refreshed; %s\n", a.ID, tokenEnd(a))

		return nil
	case len(args) == 2 && args[0] == "rm":
		if err := o.subscriptions.Remove(ctx, args[1]); err != nil {
			return err
		}

		o.printf("account %s and its tokens removed\n", args[1])

		return nil
	default:
		return usage
	}
}

// accountsLogin logs a subscription in. The owner opens a link on any machine
// with a browser; nothing is opened on the server.
func (o *owner) accountsLogin(ctx context.Context, runtime string, args []string) error {
	fs := flag.NewFlagSet("accounts login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	label := fs.String("label", "", "the account's name for the owner (default: its email)")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("%w: %s", ErrUsage, accountsUsage)
	}

	if !o.vaultReady {
		return fmt.Errorf("%w: a login's tokens are kept in the vault; set its root key first (secrets keygen)",
			vault.ErrNotConfigured)
	}

	attempt, err := o.subscriptions.Begin(ctx, runtime)
	if err != nil {
		return err
	}

	var pasted string

	if attempt.Paste {
		o.printf("Open this link in a browser on any machine and sign in:\n\n  %s\n\n"+
			"The browser ends on a page that does not load. Copy its whole address and paste it here, within %s:\n",
			attempt.URL, minutes(attempt.ExpiresIn))

		line, err := bufio.NewReader(io.LimitReader(o.in, maxPastedBytes)).ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || strings.TrimSpace(line) == "") {
			return fmt.Errorf("%w: no address was pasted", ErrUsage)
		}

		pasted = line
	} else {
		o.printf("Open this link in a browser on any machine and sign in:\n\n  %s\n\n"+
			"Then enter this code there, within %s:\n\n  %s\n\nWaiting for the login…\n",
			attempt.URL, minutes(attempt.ExpiresIn), attempt.UserCode)
	}

	a, err := o.subscriptions.Complete(ctx, attempt, pasted, *label)
	if err != nil {
		return err
	}

	o.printf("account %s (%s %s), plan %s; %s\n", a.ID, a.Runtime, a.Label, orDash(a.Plan), tokenEnd(a))

	return nil
}

func (o *owner) accountsList(ctx context.Context) error {
	accounts, err := o.subscriptions.List(ctx)
	if err != nil {
		return err
	}

	for _, a := range accounts {
		cooling := ""
		if a.CoolingUntil != nil {
			cooling = "cooling until " + a.CoolingUntil.Format(time.RFC3339)
		}

		// An account whose CLI was logged in on a node has no more to show.
		if a.State == "" {
			o.printf("%s\t%s\t%s\t%s\t%s\n", a.ID, a.Runtime, a.Label, a.Status, cooling)

			continue
		}

		o.printf("%s\t%s\t%s\t%s\t%s\t%s\tplan %s\t%s\t%s\n", a.ID, a.Runtime, a.Label, a.Status, cooling,
			a.State, orDash(a.Plan), tokenEnd(a), refreshNote(a))
	}

	return nil
}

// tokenEnd says when an account's access token ends.
func tokenEnd(a subscription.Account) string {
	if a.AccessTokenExpiresAt == nil {
		return "token end unknown"
	}

	return "token until " + a.AccessTokenExpiresAt.UTC().Format(time.RFC3339)
}

// refreshNote says why the last refresh failed, and when the next is tried.
func refreshNote(a subscription.Account) string {
	if a.Refresh.Error == "" {
		return ""
	}

	note := "last refresh failed: " + a.Refresh.Error
	if a.Refresh.RetryAt != nil {
		note += "; next try " + a.Refresh.RetryAt.UTC().Format(time.RFC3339)
	}

	return note
}

func minutes(d time.Duration) string { return fmt.Sprintf("%d minutes", int(d.Minutes())) }

func orDash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}
