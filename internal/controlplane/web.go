package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The web's search and a page's reading for workers and Bots (issue #354):
// the keys from the vault, and the agent API's and the Web search page's
// view of the gateway's metered web (modelgateway/metered_web.go).

// searchKeys reads the web search providers' keys from the vault (issue
// #393): every secret named as a key (websearch.KeyName), the first Exa key
// being web-search-exa, the name Exa's one key had. No vault, or a key whose
// secret cannot be opened, is no key: the others are still used.
func searchKeys(secrets *vault.Service, logger *slog.Logger) func(ctx context.Context) ([]websearch.Key, error) {
	return func(ctx context.Context) ([]websearch.Key, error) {
		all, err := secrets.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the vault's secrets: %w", err)
		}

		var out []websearch.Key

		for _, sec := range all {
			provider, _, ok := websearch.ParseKeyName(sec.Name)
			if !ok {
				continue
			}

			opened, err := secrets.Open(ctx, sec.Name)

			switch {
			case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrNotConfigured):
				continue
			case err != nil:
				logger.ErrorContext(ctx, "opening a web search key",
					slog.String("event", "controlplane.web_search_key_failed"), slog.String("secret", sec.Name),
					slog.String("error", err.Error()))

				continue
			}

			out = append(out, websearch.Key{Provider: provider, Name: sec.Name, Value: opened.Payload[vault.KeyValue]})
		}

		return out, nil
	}
}

// agentWeb is the web as the agent API gives it: a worker's call recorded as
// modelgateway.WorkerWeb records it, a Bot's pi's (issue #492) as
// modelgateway.BotWeb records its `hypomnema web`, on the key of its chat's
// running pi.
type agentWeb struct {
	worker modelgateway.WorkerWeb
	bot    modelgateway.BotWeb
}

func (a agentWeb) Search(ctx context.Context, who agentapi.Who, query string, limit int, provider string,
) (websearch.Answer, error) {
	if who.DispatchID != "" {
		return a.worker.Search(ctx, who.DispatchID, query, limit, provider)
	}

	return a.bot.Search(ctx, who.ChatID, query, limit, provider)
}

func (a agentWeb) Fetch(ctx context.Context, who agentapi.Who, url string) (websearch.Page, error) {
	if who.DispatchID != "" {
		return a.worker.Fetch(ctx, who.DispatchID, url)
	}

	return a.bot.Fetch(ctx, who.ChatID, url)
}

// dispatchRuntime is the runtime a Dispatch's worker runs: its
// Conversation's.
func dispatchRuntime(svc *orchestration.Service) func(ctx context.Context, dispatchID string) (string, error) {
	return func(ctx context.Context, dispatchID string) (string, error) {
		d, err := svc.GetDispatch(ctx, dispatchID)
		if err != nil {
			return "", err
		}

		c, err := svc.GetConversation(ctx, d.ConversationID)
		if err != nil {
			return "", err
		}

		return c.Runtime, nil
	}
}

// webSearchSettings is the chain and what the owner sets of it, for the Web
// search page (issues #354, #393): the search's order, keys, rests and
// usage, and the gateway's switches and order.
type webSearchSettings struct {
	web     *websearch.Service
	gateway *modelgateway.Service
}

func (w webSearchSettings) Chain(ctx context.Context) ([]websearch.Link, error) {
	return w.web.Chain(ctx)
}

func (w webSearchSettings) SetSearchProvider(ctx context.Context, provider string, on bool) error {
	return w.gateway.SetSearchProvider(ctx, provider, on)
}

func (w webSearchSettings) SetSearchOrder(ctx context.Context, providers []string) error {
	return w.gateway.SetSearchOrder(ctx, providers)
}
