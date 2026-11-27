package controlplane

import (
	"context"
	"encoding/json"
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
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// The model gateway's commands (issue #20): the model table, the keys and
// the usage. The web client has screens for them; these are commands of the
// owner on the server, as nodes and tokens are, which open the database
// directly: they work from a shell or a script there, without the client API
// or a login.

const gatewayUsage = "gateway models apply FILE | gateway models list | gateway models rm <name> | " +
	"gateway keys add <name> [--models a,b] [--endpoints search,fetch,images,audio|none] [--accounts a,b] " +
	"[--search-order codex,claude,exa] | gateway keys list | gateway keys revoke <key id> | " +
	"gateway keys set <key id> [--name NAME] [--models a,b|*] [--endpoints search,fetch,images,audio|none|*] " +
	"[--accounts a,b|*] [--search-order codex,claude,exa|settings] | " +
	"gateway keys show-value <key id> | " +
	"gateway antigravity-rewrites list | gateway antigravity-rewrites set FILE | " +
	"gateway usage [--limit N] | gateway usage --by provider|account [--period 24h|7d|30d|90d|12m]"

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
	return accountToken(g.subs.Token(keyAccounts(ctx), runtime, session, skip...))
}

// TokenFor is Token for a request for a model: only the accounts whose vendor lists it, when one does (issue #814).
func (g gatewayAccounts) TokenFor(
	ctx context.Context, runtime, model, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.TokenFor(keyAccounts(listDeadline(ctx)), runtime, model, session, skip...))
}

// keyAccounts gives the subscriptions the only accounts the gateway's request may use: its key's own (issue #1277).
func keyAccounts(ctx context.Context) context.Context {
	if ids, ok := modelgateway.KeyAccounts(ctx); ok {
		return subscription.WithAccounts(ctx, ids)
	}

	return ctx
}

// listDeadline gives the subscriptions the moment the gateway's request stops waiting for the accounts' lists of
// models: one bound for the whole request (issue #814).
func listDeadline(ctx context.Context) context.Context {
	if t, ok := modelgateway.AccountWaitEnds(ctx); ok {
		return subscription.WithListDeadline(ctx, t)
	}

	return ctx
}

func (g gatewayAccounts) FirstFree(
	ctx context.Context, runtime string, modelReset func(string) time.Time,
) (time.Time, error) {
	return g.subs.FirstFree(keyAccounts(ctx), runtime, modelReset)
}

func (g gatewayAccounts) Quota(ctx context.Context, accountID string) (map[string]float64, error) {
	return g.subs.Quota(ctx, accountID)
}

// UsedUpUntil says until when a window of a model's Antigravity group is used up (issue #576).
func (g gatewayAccounts) UsedUpUntil(ctx context.Context, accountID, model string) time.Time {
	return g.subs.UsedUpUntil(ctx, accountID, model)
}

func (g gatewayAccounts) NoteQuota(accountID string, left float64) {
	g.subs.NoteQuota(accountID, left)
}

func (g gatewayAccounts) NoteAnswer(runtime, accountID string, h http.Header) {
	g.subs.NoteAnswer(runtime, accountID, h)
}

// Serves says an account can still be sent a request for a model (issue #814).
func (g gatewayAccounts) Serves(ctx context.Context, accountID, model string) bool {
	return g.subs.Serves(ctx, accountID, model)
}

// TokenAfterRefusalFor is TokenAfterRefusal for a request for a model (issue #814).
func (g gatewayAccounts) TokenAfterRefusalFor(
	ctx context.Context, refused modelgateway.AccountToken, model string,
) (modelgateway.AccountToken, error) {
	return accountToken(g.subs.TokenAfterRefusalFor(ctx, subsToken(refused), model))
}

func (g gatewayAccounts) TokenAfterRefusal(
	ctx context.Context, refused modelgateway.AccountToken,
) (modelgateway.AccountToken, error) {
	token, err := g.subs.TokenAfterRefusal(ctx, subsToken(refused))
	if errors.Is(err, subscription.ErrTokenUnavailable) || errors.Is(err, subscription.ErrSetupToken) {
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrTokenUnavailable, err)
	}
	return accountToken(token, err)
}

func (g gatewayAccounts) MarkTokenRefused(ctx context.Context, refused modelgateway.AccountToken) error {
	return g.subs.MarkTokenRefused(ctx, subsToken(refused))
}

func (g gatewayAccounts) CoolDown(
	ctx context.Context, runtime, accountID string, until time.Time, reason string,
) error {
	return g.subs.CoolDown(ctx, runtime, accountID, until, reason)
}

// NeedsVerification marks an account Google wants verified (issue #822).
func (g gatewayAccounts) NeedsVerification(
	ctx context.Context, token modelgateway.AccountToken, url, message string,
) (time.Time, error) {
	return g.subs.NeedsVerification(ctx, subsToken(token), url, message)
}

func (g gatewayAccounts) CheckVerification(ctx context.Context, token modelgateway.AccountToken) error {
	_, err := accountToken(subscription.Token{}, g.subs.CheckVerification(ctx, subsToken(token)))

	return err
}

// Verified clears that mark once a call works under the account.
func (g gatewayAccounts) Verified(ctx context.Context, token modelgateway.AccountToken) error {
	return g.subs.Verified(ctx, subsToken(token))
}

func subsToken(token modelgateway.AccountToken) subscription.Token {
	return subscription.Token{
		AccountID: token.AccountID, ExternalID: token.ExternalID, Version: token.Version,
		VerificationRetry: token.VerificationRetry,
	}
}

// accountToken says a subscription's answer in the gateway's words.
func accountToken(t subscription.Token, err error) (modelgateway.AccountToken, error) {
	var (
		cooling *subscription.CoolingError
		verify  *subscription.VerificationError
	)

	switch {
	case errors.As(err, &verify):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrAccountResting, err)
	case errors.As(err, &cooling):
		return modelgateway.AccountToken{}, &modelgateway.CoolingError{Until: cooling.Until, ListUnknown: cooling.ListUnknown}
	case errors.Is(err, subscription.ErrNoAccount):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrNoAccount, err)
	case errors.Is(err, subscription.ErrModelNotServed):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrModelNotServed, err)
	case errors.Is(err, subscription.ErrNoModel):
		return modelgateway.AccountToken{}, fmt.Errorf("%w: %w", modelgateway.ErrNoModel, err)
	case err != nil:
		return modelgateway.AccountToken{}, err
	}

	return modelgateway.AccountToken{
		AccountID: t.AccountID, ExternalID: t.ExternalID, AccessToken: t.AccessToken, Project: t.Project,
		Plan: t.Plan, Version: t.Version, Verifying: t.Verifying,
		VerificationRetry: t.VerificationRetry,
	}, nil
}

// servedLimits and servedWires are what the subscriptions say of the models their vendors serve
// (subscription.Service).
type (
	servedLimits interface {
		ServedLimits(ctx context.Context, runtime, model string) (window, output int64, ok bool)
	}
	servedWires interface {
		ServedWire(ctx context.Context, runtime, model string) (wire string, ok bool)
	}
)

// gatewayServedLimits gives the gateway what an Antigravity account (issue #587), an OpenCode Go key (issue #854) or
// an OpenCode Zen key (issue #965) is served each model with, waiting for a list being read no later than the request's one wait (listDeadline,
// issue #814).
func gatewayServedLimits(
	subs servedLimits,
) func(ctx context.Context, kind, upstream string) (int64, int64, bool) {
	return func(ctx context.Context, kind, upstream string) (int64, int64, bool) {
		switch kind {
		case profilestore.ProviderAntigravitySubscription, profilestore.ProviderOpenCodeGo,
			profilestore.ProviderOpenCodeZen:
			runtime, _ := subscription.ProviderRuntime(kind)

			return subs.ServedLimits(keyAccounts(listDeadline(ctx)), runtime, upstream)
		default:
			return 0, 0, false
		}
	}
}

// gatewayServedWire gives the gateway the wire OpenCode Go (issue #854) or OpenCode Zen (issue #965) serves each
// model on, from its model list, waiting as gatewayServedLimits does.
func gatewayServedWire(subs servedWires) func(ctx context.Context, kind, upstream string) (string, bool) {
	return func(ctx context.Context, kind, upstream string) (string, bool) {
		if kind != profilestore.ProviderOpenCodeGo && kind != profilestore.ProviderOpenCodeZen {
			return "", false
		}

		runtime, _ := subscription.ProviderRuntime(kind)

		return subs.ServedWire(keyAccounts(listDeadline(ctx)), runtime, upstream)
	}
}

// gatewayProviderList lists every provider for the gateway, which serves each subscription's models (issue #549).
func gatewayProviderList(svc *orchestration.Service) func(ctx context.Context) ([]modelgateway.ProviderInfo, error) {
	return func(ctx context.Context) ([]modelgateway.ProviderInfo, error) {
		ps, err := svc.Profiles().ListProviders(ctx)
		if err != nil {
			return nil, err
		}

		out := make([]modelgateway.ProviderInfo, 0, len(ps))
		for _, p := range ps {
			out = append(out, modelgateway.ProviderInfo{
				Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, APIKeyRef: p.APIKeyRef, Models: p.Models,
			})
		}

		return out, nil
	}
}

// gatewayProviders gives the gateway the providers orchestration keeps.
func gatewayProviders(svc *orchestration.Service) modelgateway.Providers {
	return func(ctx context.Context, name string) (modelgateway.ProviderInfo, error) {
		p, err := svc.Profiles().GetProvider(ctx, name)
		if errors.Is(err, orchestration.ErrNotFound) {
			return modelgateway.ProviderInfo{}, fmt.Errorf("%w: provider %q", modelgateway.ErrNotFound, name)
		}

		if err != nil {
			return modelgateway.ProviderInfo{}, err
		}

		return modelgateway.ProviderInfo{
			Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, APIKeyRef: p.APIKeyRef, Models: p.Models,
			Admits: func(model string) bool { return !p.BelowFloor(model) },
		}, nil
	}
}

func (o *owner) gatewayCommand(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)

	if len(args) >= 1 && args[0] == "usage" {
		return o.gatewayUsageList(ctx, args[1:])
	}

	if len(args) >= 1 && args[0] == "antigravity-rewrites" {
		return o.gatewayRewrites(ctx, args[1:])
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
	case "keys set":
		if len(rest) < 2 {
			return usage
		}

		return o.gatewayKeysSet(ctx, rest[0], rest[1:])
	case "keys show-value":
		if len(rest) != 1 {
			return usage
		}

		value, err := o.gateway.KeyValue(ctx, rest[0])
		if err != nil {
			return err
		}

		o.printf("%s\n", value)

		return nil
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

// gatewayRewrites lists or sets the owner's find→replace rules for the Antigravity system instruction (issue
// #591). "set" reads a JSON array [{"find": "...", "with": "..."}] from a file or - for standard input, and
// replaces every rule; an empty array clears them.
func (o *owner) gatewayRewrites(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)

	switch {
	case len(args) == 1 && args[0] == "list":
		rules, err := o.gateway.AntigravityRewrites(ctx)
		if err != nil {
			return err
		}

		if len(rules) == 0 {
			o.printf("no Antigravity rewrites; the system prompt is sent unchanged\n")

			return nil
		}

		for i, r := range rules {
			o.printf("%d\t%q\t%q\n", i+1, r.Find, r.With)
		}

		return nil
	case len(args) == 2 && args[0] == "set":
		source, err := readOwnerInput(o, args[1])
		if err != nil {
			return fmt.Errorf("reading the rewrites: %w", err)
		}

		var rules []modelgateway.Rewrite
		if err := json.Unmarshal(source, &rules); err != nil {
			return fmt.Errorf("the rewrites are not a JSON array of {find, with}: %w", err)
		}

		if err := o.gateway.SetAntigravityRewrites(ctx, rules); err != nil {
			return err
		}

		o.printf("%d Antigravity rewrite(s) set\n", len(rules))

		return nil
	default:
		return usage
	}
}

// readOwnerInput reads the owner's input from a file, or standard input for "-".
func readOwnerInput(o *owner, name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(o.in)
	}

	return os.ReadFile(name) //nolint:gosec // the owner names the file
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
	endpoints := fs.String("endpoints", "*",
		"what the key may use besides chat: search, fetch, images, audio, separated by commas, or none (default: "+
			"every kind)")
	accounts := fs.String("accounts", "*", "the only accounts the key may use, separated by commas (default: every "+
		"account)")
	order := fs.String("search-order", "settings", "the web search providers the key asks first, in order, separated "+
		"by commas (default: the order of Settings)")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	allowed := []string{}
	if *models != "" {
		allowed = strings.Split(*models, ",")
	}

	routing := modelgateway.KeyRouting{SearchOrder: parseSearchOrder(*order)}
	if *accounts != modelgateway.AllAccounts {
		routing.Accounts = splitList(*accounts)
	}

	k, secret, err := o.gateway.AddRoutedKey(ctx, name, allowed, parseKeyEndpoints(*endpoints), routing)
	if err != nil {
		return err
	}

	o.printf("key %s (%s) for %s; %s%s\n", k.Name, k.ID, keyModels(k), keyEndpoints(k), keyRouting(k))
	if k.MaskedValue == "" {
		o.printf("give it to the client as its API key; the value is not stored, so copy it now:\n%s\n", secret)
	} else {
		o.printf("give it to the client as its API key; read it again with gateway keys show-value:\n%s\n", secret)
	}

	return nil
}

func (o *owner) gatewayKeysSet(ctx context.Context, id string, args []string) error {
	fs := flag.NewFlagSet("gateway keys set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "the new name")
	models := fs.String("models", "", "comma-separated models, or * for every model")
	endpoints := fs.String("endpoints", "", "comma-separated endpoint kinds, none, or * for every kind")
	accounts := fs.String("accounts", "", "comma-separated accounts, the only ones the key may use, or * for every "+
		"account")
	order := fs.String("search-order", "", "comma-separated web search providers asked first, or settings for the "+
		"order of Settings")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || fs.NFlag() == 0 {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	var change modelgateway.KeyUpdate
	fs.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "name":
			change.Name = name
		case "models":
			change.Models = []string{}
			if *models != "" {
				change.Models = strings.Split(*models, ",")
			}
		case "endpoints":
			change.Endpoints = parseKeyEndpoints(*endpoints)
			if *endpoints == "*" {
				change.Endpoints = []modelgateway.Endpoint{modelgateway.AllEndpoints}
			}
		case "accounts":
			change.Accounts = splitList(*accounts)
		case "search-order":
			change.SearchOrder = parseSearchOrder(*order)
		}
	})

	key, err := o.gateway.UpdateKey(ctx, id, change)
	if err != nil {
		return err
	}

	o.printf("key %s (%s) for %s; %s%s\n", key.Name, key.ID, keyModels(key), keyEndpoints(key), keyRouting(key))

	return nil
}

func parseKeyEndpoints(value string) []modelgateway.Endpoint {
	switch value {
	case "*":
		return nil
	case "none":
		return []modelgateway.Endpoint{}
	default:
		var kinds []modelgateway.Endpoint
		for endpoint := range strings.SplitSeq(value, ",") {
			kinds = append(kinds, modelgateway.Endpoint(strings.TrimSpace(endpoint)))
		}

		return kinds
	}
}

// splitList is a list given separated by commas; empty is a list of none.
func splitList(value string) []string {
	out := []string{}

	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}

	return out
}

// parseSearchOrder is a key's own search order; "settings" (or nothing) is the global order (issue #1277).
func parseSearchOrder(value string) []string {
	if strings.TrimSpace(value) == "settings" {
		return []string{}
	}

	return splitList(value)
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

		o.printf("%s\t%s\t%s\t%s\t%s%s\n", k.ID, k.Name, state, keyModels(k), keyEndpoints(k), keyRouting(k))
	}

	return nil
}

func keyModels(k modelgateway.Key) string {
	if k.Allows(modelgateway.AllModels) {
		return "every model"
	}

	return strings.Join(k.Models, ", ")
}

// keyRouting says a key's own routing (issue #1277), after what it may use; nothing for a key routed as the others.
func keyRouting(k modelgateway.Key) string {
	var out string

	if k.OwnAccounts() {
		ids := make([]string, 0, len(k.Accounts))
		for _, a := range k.Accounts {
			ids = append(ids, a.ID)
		}

		out += "; only accounts " + orDash(strings.Join(ids, ", "))
	}

	if len(k.SearchOrder) > 0 {
		out += "; searches " + strings.Join(k.SearchOrder, ", ") + " first"
	}

	return out
}

// keyEndpoints says what a key may use besides chat.
func keyEndpoints(k modelgateway.Key) string {
	endpoints := k.Endpoints
	if endpoints == nil {
		endpoints = modelgateway.Endpoints
	}

	if len(endpoints) == 0 {
		return "chat only"
	}

	out := make([]string, 0, len(endpoints))
	for _, e := range endpoints {
		out = append(out, string(e))
	}

	return strings.Join(out, ", ")
}

// gatewayUsageList prints the latest requests the gateway served.
func (o *owner) gatewayUsageList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("gateway usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	limit := fs.Int("limit", 50, "how many of the latest requests to show")
	by := fs.String("by", "", "provider or account: the text models' speed and reliability, as the Usage page")
	period := fs.String("period", "7d", "with --by: 24h, 7d, 30d, 90d or 12m")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	if *by != "" {
		return o.gatewayPerformance(ctx, *by, *period)
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

// gatewayPerformance prints the Usage page's Performance table (issue #856): each provider's model, or each
// subscription account, with its requests, success rate, time to first token, latency, tokens a second and cost.
// A period read from the daily ledger has averages in place of the percentiles.
func (o *owner) gatewayPerformance(ctx context.Context, by, period string) error {
	if by != "provider" && by != "account" {
		return fmt.Errorf("%w: %s", ErrUsage, gatewayUsage)
	}

	report, err := o.gateway.Usage(ctx, period, 0)
	if err != nil {
		return err
	}

	groups, head := report.ByProvider, "PROVIDER\tMODEL"
	if by == "account" {
		groups, head = report.SpeedByAccount, "PROVIDER\tACCOUNT"
	}

	if report.Ledger {
		o.printf("%s\tREQUESTS\tOK%%\tTTFT AVG\tLATENCY AVG\tTOK/S\tCOST\n", head)
	} else {
		o.printf("%s\tREQUESTS\tOK%%\tTTFT P50\tTTFT P95\tLATENCY P50\tLATENCY P95\tTOK/S P50\tCOST\n", head)
	}

	for _, g := range groups {
		name := g.Model
		if by == "account" {
			name = g.AccountID
		}

		cols := []string{g.Provider, name, fmt.Sprint(g.Requests), percent(g.Performance)}
		if report.Ledger {
			cols = append(cols, millis(g.TTFTAvg), millis(g.LatencyAvg))
		} else {
			cols = append(cols, millis(g.TTFTP50), millis(g.TTFTP95), millis(g.LatencyP50), millis(g.LatencyP95))
		}

		rate := "-"
		if g.TokensPerSecond != nil {
			rate = fmt.Sprintf("%.1f", *g.TokensPerSecond)
		}

		cols = append(cols, rate, fmt.Sprintf("$%.4f", float64(g.CostMicros)/1e6))
		o.printf("%s\n", strings.Join(cols, "\t"))
	}

	return nil
}

func percent(p modelgateway.Performance) string {
	rate, ok := p.SuccessRate()
	if !ok {
		return "-"
	}

	return fmt.Sprintf("%.1f", rate*100)
}

func millis(d *time.Duration) string {
	if d == nil {
		return "-"
	}

	return d.Round(time.Millisecond).String()
}
