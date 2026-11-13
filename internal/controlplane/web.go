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

// exaKey reads Exa's API key from the vault, under the name the
// configuration gives (HYPOMNEMA_WEB_SEARCH_EXA_SECRET): no such secret, or
// no vault, is no key, and Exa is asked through its public MCP.
func exaKey(secrets *vault.Service, name string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		opened, err := secrets.Open(ctx, name)

		switch {
		case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrNotConfigured):
			return "", nil
		case err != nil:
			return "", fmt.Errorf("opening secret %q: %w", name, err)
		}

		return opened.Payload[vault.KeyValue], nil
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
