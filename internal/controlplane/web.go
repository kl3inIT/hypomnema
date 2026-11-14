package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The web's search and a page's reading for workers and Bots (issue #354):
// one websearch.Service, which the model gateway serves on /v1/search and
// /v1/web/fetch, the agent API as a worker's tools and the client API as a
// Bot's commands. Each call is recorded on the gateway key of whoever asked,
// as OmniRoute records a search on its key: the gateway records its own; a
// worker's is its Dispatch's key, and a Bot's the key of its chat's pi.

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

// meteredWeb records each web call on a gateway key.
type meteredWeb struct {
	web     *websearch.Service
	gateway *modelgateway.Service
	logger  *slog.Logger
}

// search runs a search and records it on the key keyOf names, when it names one.
func (m meteredWeb) search(ctx context.Context, keyOf func(context.Context) (string, bool), query string,
	limit int, provider string,
) (websearch.Answer, error) {
	start := time.Now()
	a, err := m.web.Search(ctx, query, limit, provider)
	m.record(ctx, keyOf, modelgateway.WebCall{
		Model: modelgateway.ModelWebSearch, Provider: a.Provider, Attempts: len(a.Errors) + 1, Err: err,
		Duration: time.Since(start),
	})

	return a, err
}

// fetch reads a page and records it on the key keyOf names, when it names one.
func (m meteredWeb) fetch(ctx context.Context, keyOf func(context.Context) (string, bool), url string,
) (websearch.Page, error) {
	start := time.Now()
	p, err := m.web.Fetch(ctx, url)
	m.record(ctx, keyOf, modelgateway.WebCall{
		Model: modelgateway.ModelWebFetch, Attempts: 1, Err: err, Duration: time.Since(start),
	})

	return p, err
}

func (m meteredWeb) record(ctx context.Context, keyOf func(context.Context) (string, bool), c modelgateway.WebCall) {
	ctx = context.WithoutCancel(ctx)

	keyID, ok := keyOf(ctx)
	if !ok {
		return
	}

	c.KeyID = keyID
	if err := m.gateway.RecordWebCall(ctx, c); err != nil {
		m.logger.ErrorContext(ctx, "recording a web call",
			slog.String("event", "controlplane.web_usage_failed"), slog.String("error", err.Error()))
	}
}

// workerWeb is the web as the agent API gives it to a Dispatch's worker:
// recorded on the Dispatch's gateway key, when it has one (a worker whose
// model the gateway serves).
type workerWeb struct{ meteredWeb }

func (w workerWeb) Search(ctx context.Context, dispatchID, query string, limit int, provider string,
) (websearch.Answer, error) {
	return w.search(ctx, w.dispatchKey(dispatchID), query, limit, provider)
}

func (w workerWeb) Fetch(ctx context.Context, dispatchID, url string) (websearch.Page, error) {
	return w.fetch(ctx, w.dispatchKey(dispatchID), url)
}

func (w workerWeb) dispatchKey(dispatchID string) func(context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) {
		k, err := w.gateway.DispatchKey(ctx, dispatchID)
		if err != nil {
			if !errors.Is(err, modelgateway.ErrNotFound) {
				w.logger.ErrorContext(ctx, "reading a dispatch's gateway key",
					slog.String("event", "controlplane.web_key_failed"), slog.String("dispatch_id", dispatchID),
					slog.String("error", err.Error()))
			}

			return "", false
		}

		return k.ID, true
	}
}

// chatKeyHolder is what holds the gateway key of each chat's running pi:
// the coordinator's supervisor.
type chatKeyHolder interface {
	Key(chatID string) (string, bool)
}

// botWeb is the web as the client API gives it to a token: recorded on the
// key of the chat's running pi when a Bot's pi asks; another token's call,
// which belongs to no chat, is not recorded.
type botWeb struct {
	meteredWeb
	chats chatKeyHolder
}

func (b botWeb) Search(ctx context.Context, chatID, query string, limit int, provider string,
) (websearch.Answer, error) {
	return b.search(ctx, b.chatKey(chatID), query, limit, provider)
}

func (b botWeb) Fetch(ctx context.Context, chatID, url string) (websearch.Page, error) {
	return b.fetch(ctx, b.chatKey(chatID), url)
}

func (b botWeb) chatKey(chatID string) func(context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) {
		if chatID == "" {
			return "", false
		}

		secret, ok := b.chats.Key(chatID)
		if !ok {
			return "", false
		}

		k, err := b.gateway.Authenticate(ctx, secret)
		if err != nil {
			return "", false
		}

		return k.ID, true
	}
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
