package controlplane

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// The owner's commands that run on the server: they open the same database as
// `hypomnema serve`, whose writes SQLite serializes with theirs. Nodes,
// accounts and client tokens are set up here, since a client needs a token
// before it can call the client API. Everything else goes through that API
// (internal/controlplane/clientcli).

// ErrUsage marks a command line that is wrong.
var ErrUsage = errors.New("usage")

// Command runs one owner command: nodes, accounts or tokens.
func Command(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}

	db, err := sqlite.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at exit; nothing is left to report it to.

	if err := orchestration.CheckSchema(ctx, db); err != nil {
		return err
	}

	o := &owner{
		svc: orchestration.New(db, platform.SystemClock{}, platform.NewID),
		out: stdout,
	}

	switch args[0] {
	case "nodes":
		return o.nodes(ctx, args[1:])
	case "accounts":
		return o.accounts(ctx, args[1:])
	case "tokens":
		return o.tokens(ctx, args[1:])
	default:
		return ErrUsage
	}
}

type owner struct {
	svc *orchestration.Service
	out io.Writer
}

func (o *owner) printf(format string, a ...any) {
	_, _ = fmt.Fprintf(o.out, format, a...)
}

func (o *owner) nodes(ctx context.Context, args []string) error {
	switch {
	case len(args) >= 2 && args[0] == "add":
		fs := flag.NewFlagSet("nodes add", flag.ContinueOnError)
		limit := fs.Int("max", 0, "Dispatches the node runs at once (0: the default)")

		if err := fs.Parse(args[2:]); err != nil {
			return fmt.Errorf("%w: %w", ErrUsage, err)
		}

		n, token, err := o.svc.AddNode(ctx, args[1], *limit)
		if err != nil {
			return err
		}

		o.printf("node %s (%s), up to %d Dispatches\n", n.Name, n.ID, n.MaxDispatches)
		o.printf("token, shown only now; put it in the node's configuration and nowhere else:\n%s\n", token)

		return nil
	case len(args) == 1 && args[0] == "list":
		nodes, err := o.svc.ListNodes(ctx)
		if err != nil {
			return err
		}

		for _, n := range nodes {
			state := "offline"
			if n.Connected {
				state = "connected"
			}

			o.printf("%s\t%s\t%s\t%s\t%d/%d\t%s\n", n.ID, n.Name, n.Status, state, n.Active, n.MaxDispatches,
				runtimes(n.Runtimes))
		}

		return nil
	default:
		return ErrUsage
	}
}

func runtimes(rs []orchestration.RuntimeInfo) string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Runtime+" "+r.Version)
	}

	return strings.Join(out, ", ")
}

func (o *owner) accounts(ctx context.Context, args []string) error {
	switch {
	case len(args) == 3 && args[0] == "add":
		a, err := o.svc.AddAccount(ctx, args[1], args[2])
		if err != nil {
			return err
		}

		o.printf("account %s (%s %s)\n", a.ID, a.Runtime, a.Label)

		return nil
	case len(args) == 1 && args[0] == "list":
		accounts, err := o.svc.ListAccounts(ctx)
		if err != nil {
			return err
		}

		for _, a := range accounts {
			cooling := ""
			if a.CoolingUntil != nil {
				cooling = "cooling until " + a.CoolingUntil.Format(time.RFC3339)
			}

			o.printf("%s\t%s\t%s\t%s\t%s\n", a.ID, a.Runtime, a.Label, a.Status, cooling)
		}

		return nil
	default:
		return ErrUsage
	}
}

// tokens adds, lists and revokes the client API's tokens.
func (o *owner) tokens(ctx context.Context, args []string) error {
	switch {
	case len(args) >= 2 && args[0] == "add":
		fs := flag.NewFlagSet("tokens add", flag.ContinueOnError)
		kind := fs.String("kind", string(bearertoken.Owner), "owner (everything) or coordinator (its own Runs)")

		if err := fs.Parse(args[2:]); err != nil || fs.NArg() != 0 {
			return fmt.Errorf("%w: tokens add <name> [--kind owner|coordinator]", ErrUsage)
		}

		t, secret, err := o.svc.AddToken(ctx, args[1], bearertoken.Kind(*kind))
		if err != nil {
			return err
		}

		o.printf("token %s (%s, %s)\n", t.Name, t.ID, t.Kind)
		o.printf("shown only now; put it in a file for `hypomnema login --token-file` and nowhere else:\n%s\n", secret)

		return nil
	case len(args) == 1 && args[0] == "list":
		tokens, err := o.svc.ListTokens(ctx)
		if err != nil {
			return err
		}

		for _, t := range tokens {
			state := "active"
			if t.RevokedAt != nil {
				state = "revoked " + t.RevokedAt.Format(time.RFC3339)
			}

			o.printf("%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Kind, state)
		}

		return nil
	case len(args) == 2 && args[0] == "revoke":
		if err := o.svc.RevokeToken(ctx, args[1]); err != nil {
			return err
		}

		o.printf("token %s revoked\n", args[1])

		return nil
	default:
		return ErrUsage
	}
}
