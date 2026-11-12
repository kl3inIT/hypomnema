package controlplane

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// The model gateway's commands (issue #20): the model table and the keys.
// They are commands of the owner on the server, as nodes and tokens are,
// until the web client has its screens.

const gatewayUsage = "gateway models apply FILE | gateway models list | gateway models rm <name> | " +
	"gateway keys add <name> [--models a,b] | gateway keys list | gateway keys revoke <key id> | " +
	"gateway usage [--limit N]"

// gatewaySecrets gives the gateway a provider's key from the vault. A
// provider served by the gateway keeps its key there and names it as
// vault:<name>; an environment variable or a file is for a CLI on its own
// machine, not for the control plane.
func gatewaySecrets(v *vault.Service) modelgateway.Secrets {
	return func(ctx context.Context, ref string) (string, error) {
		name, ok := vault.NameFromRef(ref)
		if !ok {
			return "", fmt.Errorf("%w: api_key_ref %q is not vault:<name>", modelgateway.ErrInvalid, ref)
		}

		opened, err := v.Open(ctx, name)
		if err != nil {
			return "", err
		}

		return opened.Payload[vault.KeyValue], nil
	}
}

// gatewayAccounts gives the gateway's built-in providers the accounts logged
// in on the control plane.
type gatewayAccounts struct{ subs *subscription.Service }

func (g gatewayAccounts) Token(
	ctx context.Context, runtime, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.Token(ctx, runtime, session, skip...))
}

func (g gatewayAccounts) Quota(ctx context.Context, accountID string) (map[string]float64, error) {
	return g.subs.Quota(ctx, accountID)
}

func (g gatewayAccounts) NoteQuota(accountID string, left float64) {
	g.subs.NoteQuota(accountID, left)
}

func (g gatewayAccounts) NoteAnswer(runtime, accountID string, h http.Header) {
	g.subs.NoteAnswer(runtime, accountID, h)
}

func (g gatewayAccounts) TokenAfterRefusal(
	ctx context.Context, refused modelgateway.AccountToken,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.TokenAfterRefusal(ctx, subscription.Token{
		AccountID: refused.AccountID, ExternalID: refused.ExternalID, Version: refused.Version,
	}))
}

func (g gatewayAccounts) CoolDown(
	ctx context.Context, runtime, accountID string, until time.Time, reason string,
) error {
	return g.subs.CoolDown(ctx, runtime, accountID, until, reason)
}

// accountToken says a subscription's answer in the gateway's words.
func accountToken(t subscription.Token, err error) (modelgateway.AccountToken, error) {
	var cooling *subscription.CoolingError

	switch {
	case errors.As(err, &cooling):
		return modelgateway.AccountToken{}, &modelgateway.CoolingError{Until: cooling.Until}
	case errors.Is(err, subscription.ErrNoAccount):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrNoAccount, err)
	case err != nil:
		return modelgateway.AccountToken{}, err
	}

	return modelgateway.AccountToken{
		AccountID: t.AccountID, ExternalID: t.ExternalID, AccessToken: t.AccessToken, Project: t.Project,
		Version: t.Version,
	}, nil
}

// gatewayProviders gives the gateway the providers orchestration keeps.
func gatewayProviders(svc *orchestration.Service) modelgateway.Providers {
	return func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
		p, err := svc.GetProvider(ctx, name)
		if errors.Is(err, orchestration.ErrNotFound) {
			return modelgateway.ProviderInfo{}, fmt.Errorf("%w: provider %q", modelgateway.ErrNotFound, name)
		}

		if err != nil {
			return modelgateway.ProviderInfo{}, err
		}

		return modelgateway.ProviderInfo{
			Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, APIKeyRef: p.APIKeyRef, Models: p.Models,
		}, nil
	}
}

func (o *owner) gatewayCommand(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)

	if len(args) >= 1 && args[0] == "usage" {
		return o.gatewayUsageList(ctx, args[1:])
	}

	if len(args) < 2 {
		return usage
	}

	rest := args[2:]

	switch args[0] + " " + args[1] {
	case "models apply":
		if len(rest) != 1 {
			return usage
		}

		return o.gatewayModelsApply(ctx, rest[0])
	case "models list":
		if len(rest) != 0 {
			return usage
		}

		return o.gatewayModelsList(ctx)
	case "models rm":
		if len(rest) != 1 {
			return usage
		}

		if err := o.gateway.DeleteModel(ctx, rest[0]); err != nil {
			return err
		}

		o.printf("model %s removed\n", rest[0])

		return nil
	case "keys add":
		if len(rest) == 0 {
			return usage
		}

		return o.gatewayKeysAdd(ctx, rest[0], rest[1:])
	case "keys list":
		if len(rest) != 0 {
			return usage
		}

		return o.gatewayKeysList(ctx)
	case "keys revoke":
		if len(rest) != 1 {
			return usage
		}

		if err := o.gateway.RevokeKey(ctx, rest[0]); err != nil {
			return err
		}

		o.printf("key %s revoked\n", rest[0])

		return nil
	default:
		return usage
	}
}

func (o *owner) gatewayModelsApply(ctx context.Context, file string) error {
	source, err := os.ReadFile(file) //nolint:gosec // the owner names the file holding the model
	if err != nil {
		return fmt.Errorf("reading the model: %w", err)
	}

	m, err := o.gateway.ApplyModel(ctx, string(source))
	if err != nil {
		return err
	}

	o.printf("model %s (%s) served from %s\n", m.Name, m.Capability, targetList(m.Targets))

	return nil
}

func (o *owner) gatewayModelsList(ctx context.Context) error {
	models, err := o.gateway.ListModels(ctx)
	if err != nil {
		return err
	}

	for _, m := range models {
		o.printf("%s\t%s\t%s\n", m.Name, m.Capability, targetList(m.Targets))
	}

	return nil
}

func targetList(ts []modelgateway.Target) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Provider+":"+t.UpstreamModel)
	}

	return strings.Join(out, ", ")
}

func (o *owner) gatewayKeysAdd(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet("gateway keys add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	models := fs.String("models", "", "the models the key may use, separated by commas (default: every model)")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	var allowed []string
	if *models != "" {
		allowed = strings.Split(*models, ",")
	}

	k, secret, err := o.gateway.AddKey(ctx, name, allowed)
	if err != nil {
		return err
	}

	o.printf("key %s (%s) for %s\n", k.Name, k.ID, keyModels(k))
	o.printf("shown only now; give it to the client as its API key and keep it nowhere else:\n%s\n", secret)

	return nil
}

func (o *owner) gatewayKeysList(ctx context.Context) error {
	keys, err := o.gateway.ListKeys(ctx)
	if err != nil {
		return err
	}

	for _, k := range keys {
		state := "active"
		if k.RevokedAt != nil {
			state = "revoked " + k.RevokedAt.Format(time.RFC3339)
		}

		o.printf("%s\t%s\t%s\t%s\n", k.ID, k.Name, state, keyModels(k))
	}

	return nil
}

func keyModels(k modelgateway.Key) string {
	if k.Allows(modelgateway.AllModels) {
		return "every model"
	}

	return strings.Join(k.Models, ", ")
}

// gatewayUsageList prints the latest requests the gateway served.
func (o *owner) gatewayUsageList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("gateway usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	limit := fs.Int("limit", 50, "how many of the latest requests to show")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	rows, err := o.gateway.ListUsage(ctx, *limit)
	if err != nil {
		return err
	}

	for _, u := range rows {
		tokens := fmt.Sprintf("in %d (cached %d) out %d (reasoning %d)",
			u.Tokens.Prompt, u.Tokens.Cached, u.Tokens.Completion, u.Tokens.Reasoning)
		if u.Tokens.Estimated {
			tokens += " estimated"
		}

		o.printf("%s\t%s\t%s\t%s:%s\t%s %d\t%s\t%s\n", u.CreatedAt.Format(time.RFC3339), u.KeyName, u.Model,
			u.Provider, u.UpstreamModel, u.Outcome, u.Status, tokens, u.Duration.Round(time.Millisecond))
	}

	return nil
}
