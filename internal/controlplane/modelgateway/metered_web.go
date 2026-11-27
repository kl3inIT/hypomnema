package modelgateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The web's search and a page's reading for workers and Bots (issue #354): one websearch.Service, which the gateway
// serves on /v1/search and /v1/web/fetch, the agent API as a worker's and a Bot's pi's tools and the client API as
// commands. Each call is recorded on the gateway key of whoever asked, as OmniRoute records a search on its key: the
// gateway records its own; a worker's is its Dispatch's key, and a Bot's the key of its chat's pi.

// MeteredWeb records each web call on a gateway key.
type MeteredWeb struct {
	Web     *websearch.Service
	Gateway *Service
	Logger  *slog.Logger
}

// search runs a search and records it on the key keyOf names, when it names one.
func (m MeteredWeb) search(ctx context.Context, keyOf func(context.Context) (string, bool), query string,
	limit int, provider string,
) (websearch.Answer, error) {
	start := time.Now()
	a, err := m.Web.Search(ctx, query, limit, provider)
	m.record(ctx, keyOf, WebCall{
		Model: ModelWebSearch, Provider: a.Provider, Attempts: len(a.Errors) + 1, Err: err,
		Duration: time.Since(start),
	})

	return a, err
}

// fetch reads a page and records it on the key keyOf names, when it names one.
func (m MeteredWeb) fetch(ctx context.Context, keyOf func(context.Context) (string, bool), url string,
) (websearch.Page, error) {
	start := time.Now()
	p, err := m.Web.Fetch(ctx, url)
	m.record(ctx, keyOf, WebCall{
		Model: ModelWebFetch, Attempts: 1, Err: err, Duration: time.Since(start),
	})

	return p, err
}

func (m MeteredWeb) record(ctx context.Context, keyOf func(context.Context) (string, bool), c WebCall) {
	ctx = context.WithoutCancel(ctx)

	keyID, ok := keyOf(ctx)
	if !ok {
		return
	}

	c.KeyID = keyID
	if err := m.Gateway.RecordWebCall(ctx, c); err != nil {
		m.Logger.ErrorContext(ctx, "recording a web call",
			slog.String("event", "controlplane.web_usage_failed"), slog.String("error", err.Error()))
	}
}

// WorkerWeb is the web as the agent API gives it to a Dispatch's worker: recorded on the Dispatch's gateway key, when
// it has one (a worker whose model the gateway serves).
type WorkerWeb struct{ MeteredWeb }

func (w WorkerWeb) Search(ctx context.Context, dispatchID, query string, limit int, provider string,
) (websearch.Answer, error) {
	return w.search(ctx, w.dispatchKey(dispatchID), query, limit, provider)
}

func (w WorkerWeb) Fetch(ctx context.Context, dispatchID, url string) (websearch.Page, error) {
	return w.fetch(ctx, w.dispatchKey(dispatchID), url)
}

func (w WorkerWeb) dispatchKey(dispatchID string) func(context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) { return dispatchKeyID(ctx, w.Gateway, w.Logger, dispatchID) }
}

// dispatchKeyID is the id of a Dispatch's gateway key; false for a Dispatch that has none (a worker whose model the
// gateway does not serve).
func dispatchKeyID(ctx context.Context, gateway *Service, logger *slog.Logger, dispatchID string,
) (string, bool) {
	k, err := gateway.DispatchKey(ctx, dispatchID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			logger.ErrorContext(ctx, "reading a dispatch's gateway key",
				slog.String("event", "controlplane.web_key_failed"), slog.String("dispatch_id", dispatchID),
				slog.String("error", err.Error()))
		}

		return "", false
	}

	return k.ID, true
}

// ChatKeys is what holds the gateway key of each chat's running pi: the coordinator's supervisor.
type ChatKeys interface {
	Key(chatID string) (string, bool)
}

// BotWeb is the web as the client API gives it to a token: recorded on the key of the chat's running pi when a Bot's
// pi asks; another token's call, which belongs to no chat, is not recorded.
type BotWeb struct {
	MeteredWeb

	Chats ChatKeys
}

func (b BotWeb) Search(ctx context.Context, chatID, query string, limit int, provider string,
) (websearch.Answer, error) {
	return b.search(ctx, b.chatKey(chatID), query, limit, provider)
}

func (b BotWeb) Fetch(ctx context.Context, chatID, url string) (websearch.Page, error) {
	return b.fetch(ctx, b.chatKey(chatID), url)
}

func (b BotWeb) chatKey(chatID string) func(context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) { return chatKeyID(ctx, b.Gateway, b.Chats, chatID) }
}

// chatKeyID is the id of the gateway key of a chat's running pi; false for no chat, or a chat whose pi runs no more.
func chatKeyID(ctx context.Context, gateway *Service, chats ChatKeys, chatID string) (string, bool) {
	if chatID == "" {
		return "", false
	}

	secret, ok := chats.Key(chatID)
	if !ok {
		return "", false
	}

	k, err := gateway.Authenticate(ctx, secret)
	if err != nil {
		return "", false
	}

	return k.ID, true
}
