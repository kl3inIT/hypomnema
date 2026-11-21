package webpush

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// ErrUnavailable says this control plane sends no push: it has no https public URL, the VAPID subject Apple's push
// service needs (OpenClaw's CHANGELOG/2026.5.22.md:224, Coder's access URL, cli/server.go:1025).
var ErrUnavailable = fmt.Errorf("%w: web push needs the control plane's https public URL (HYPOMNEMA_PUBLIC_URL)",
	orchestration.ErrUnavailable)

// ErrGone says the push service no longer knows the browser: its subscription was deleted.
var ErrGone = fmt.Errorf("%w: the push service no longer knows this device; it was removed", orchestration.ErrNotFound)

const (
	// ttl is how long a push service keeps a push for a browser that is away: five minutes, OpenClaw's
	// EVENT_PUSH_TTL_SECONDS for every event push (src/gateway/event-web-push.ts:42), the design's TTL for
	// questions and Approvals (issue #1047). What waits longer is still in the app.
	ttl = 300
	// requestTimeout bounds one exchange with a push service, body included; maxBody what of its answer is read
	// (Coder's pushRequestTimeout and MaxResponseBodyBytes, coderd/webpush/webpush.go).
	requestTimeout = 30 * time.Second
	maxBody        = 64 << 10
	// declarative is the member that makes a payload a Declarative Web Push message, which Safari shows by itself
	// and opens at its navigate URL (WebKit's Declarative Web Push; the owner's Second Bite, push.go:347-367).
	declarative = 8030
)

// retryWaits are the waits before each new try of a push the service refused for now (429 or a 5xx) or did not
// answer: a few tries, as the owner's Second Bite retries its outbox (push_sender.go:423-433).
var retryWaits = []time.Duration{time.Second, 4 * time.Second}

// Store is what the sender reads and writes of the orchestration database.
type Store interface {
	ListPushSubscriptions(ctx context.Context) ([]orchestration.PushSubscription, error)
	DeletePushSubscriptionByEndpoint(ctx context.Context, endpoint string) error
	QueuedPushes(ctx context.Context) ([]orchestration.PushNotice, error)
	PushSent(ctx context.Context, seq int64) error
	// Subscribe is signaled after commits (orchestration.Service.Subscribe).
	Subscribe(ctx context.Context) <-chan struct{}
}

// KeyPair is the VAPID key pair (Keys).
type KeyPair interface {
	Pair(ctx context.Context) (private, public string, err error)
}

// Config is what a Sender needs.
type Config struct {
	Store Store
	Keys  KeyPair
	// PublicURL is the control plane's URL: the VAPID subject and the origin of the page a push opens.
	PublicURL string
	// Client sends to the push services; nil is a client with requestTimeout.
	Client *http.Client
	Logger *slog.Logger
	// RetryWaits replaces retryWaits, for tests.
	RetryWaits []time.Duration
}

// Sender pushes what waits on the owner to his browsers.
type Sender struct {
	store     Store
	keys      KeyPair
	publicURL string
	client    *http.Client
	logger    *slog.Logger
	waits     []time.Duration
}

// New returns a Sender.
func New(cfg Config) *Sender {
	s := &Sender{
		store: cfg.Store, keys: cfg.Keys, publicURL: strings.TrimSuffix(cfg.PublicURL, "/"), client: cfg.Client,
		logger: cfg.Logger, waits: cfg.RetryWaits,
	}

	if s.client == nil {
		s.client = &http.Client{Timeout: requestTimeout}
	}

	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}

	if s.waits == nil {
		s.waits = retryWaits
	}

	return s
}

// Available says whether the sender can push: the control plane has an https public URL.
func (s *Sender) Available() bool { return strings.HasPrefix(s.publicURL, "https://") }

// PublicKey is the VAPID public key a browser subscribes with, made on the first read.
func (s *Sender) PublicKey(ctx context.Context) (string, error) {
	if !s.Available() {
		return "", ErrUnavailable
	}

	_, public, err := s.keys.Pair(ctx)

	return public, err
}

// Run sends what is queued, then again after each commit, until ctx ends. The queue is written in the transaction
// of the item that waits, so a push goes only after its item is committed, and a push queued while the control
// plane was stopped goes when it starts (Second Bite's outbox after commit).
func (s *Sender) Run(ctx context.Context) {
	wake := s.store.Subscribe(ctx)

	for {
		s.drain(ctx)

		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

// drain sends every queued push.
func (s *Sender) drain(ctx context.Context) {
	for ctx.Err() == nil {
		notices, err := s.store.QueuedPushes(ctx)
		if err != nil {
			s.fail(ctx, "reading the queued pushes", err)

			return
		}

		if len(notices) == 0 {
			return
		}

		for _, n := range notices {
			if err := s.send(ctx, n); err != nil {
				s.fail(ctx, "sending a push", err)

				return
			}
		}
	}
}

// send pushes one notice to every browser, then takes it off the queue. A notice whose item no longer waits, or
// with no browser to tell, is taken off unsent. An error leaves it queued for the next wake.
func (s *Sender) send(ctx context.Context, n orchestration.PushNotice) error {
	if !n.Waiting || !s.Available() {
		return s.store.PushSent(ctx, n.Seq)
	}

	subs, err := s.store.ListPushSubscriptions(ctx)
	if err != nil {
		return err
	}

	if len(subs) == 0 {
		return s.store.PushSent(ctx, n.Seq)
	}

	private, public, err := s.keys.Pair(ctx)
	if err != nil {
		return err
	}

	msg, err := s.message(string(n.Kind)+"-"+n.ItemID, n.Title, n.Body, n.Path)
	if err != nil {
		return err
	}

	// Every browser at once, as Coder's Dispatch (coderd/webpush/webpush.go); a browser that fails after its tries
	// is logged and not tried again, as Coder drops what it cannot deliver.
	var wg sync.WaitGroup
	for _, sub := range subs {
		wg.Go(func() {
			if err := s.deliver(ctx, msg, sub, private, public); err != nil && ctx.Err() == nil {
				s.logger.WarnContext(ctx, "a push was not delivered",
					slog.String("event", "webpush.delivery_failed"), slog.String("kind", string(n.Kind)),
					slog.String("item", n.ItemID), slog.String("subscription", sub.ID), slog.String("error", err.Error()))
			}
		})
	}

	wg.Wait()

	// Stopped midway: the push stays queued and goes again at the next start.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("stopped while pushing: %w", err)
	}

	return s.store.PushSent(ctx, n.Seq)
}

// Test sends a test push to one browser, as Coder's POST /users/me/webpush/test (coderd/webpush.go): the owner sees
// at once that his device is told.
func (s *Sender) Test(ctx context.Context, sub orchestration.PushSubscription) error {
	if !s.Available() {
		return ErrUnavailable
	}

	private, public, err := s.keys.Pair(ctx)
	if err != nil {
		return err
	}

	msg, err := s.message("test", "Notifications work", "This device will be told when something waits on you.",
		"/settings/notifications")
	if err != nil {
		return err
	}

	return s.deliver(ctx, msg, sub, private, public)
}

// message is a push in the Declarative Web Push form: Safari shows it by itself and opens navigate; the service
// worker (web/public/sw.js) shows it in other browsers. The tag names the item, so a newer push of the same item
// replaces the one shown (OpenClaw's tag, event-web-push.ts). The words are the item's own: only the owner's own
// devices subscribe (owner, 2026-11-21, OpenClaw's detailed level).
type message struct {
	tag     string
	payload []byte
}

type declarativePush struct {
	WebPush      int          `json:"web_push"`
	Notification notification `json:"notification"`
}

type notification struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Navigate string `json:"navigate"`
	Tag      string `json:"tag"`
}

func (s *Sender) message(item, title, body, path string) (message, error) {
	tag := "hypomnema-" + item

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	// The words go as they are: HTML escapes would take payload room for nothing.
	enc.SetEscapeHTML(false)

	if err := enc.Encode(declarativePush{WebPush: declarative, Notification: notification{
		Title: title, Body: body, Navigate: s.publicURL + path, Tag: tag,
	}}); err != nil {
		return message{}, fmt.Errorf("encoding a push: %w", err)
	}

	return message{tag: tag, payload: bytes.TrimSpace(buf.Bytes())}, nil
}

// topic collapses a pending push of the same item at the push service: the hash of the tag in base64url, 32
// characters, as OpenClaw's (event-web-push.ts:283) and the owner's Second Bite's Topic per order.
func topic(tag string) string {
	sum := sha256.Sum256([]byte(tag))

	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

// deliver sends one push to one browser, trying again a few times when the service refuses it for now (429, 5xx)
// or does not answer. A browser the service no longer knows (404, 410) is deleted, as all four references do.
func (s *Sender) deliver(ctx context.Context, msg message, sub orchestration.PushSubscription,
	private, public string,
) error {
	for attempt := 0; ; attempt++ {
		status, err := s.post(ctx, msg, sub, private, public)

		switch {
		case err == nil && status >= 200 && status < 300:
			return nil
		case err == nil && (status == http.StatusNotFound || status == http.StatusGone):
			if err := s.store.DeletePushSubscriptionByEndpoint(ctx, sub.Endpoint); err != nil {
				return err
			}

			return fmt.Errorf("%w (%d from %s)", ErrGone, status, host(sub.Endpoint))
		case err == nil && status != http.StatusTooManyRequests && status < 500:
			return fmt.Errorf("the push service at %s refused the push: %d", host(sub.Endpoint), status)
		case attempt >= len(s.waits):
			if err != nil {
				return err
			}

			return fmt.Errorf("the push service at %s answered %d after %d tries", host(sub.Endpoint), status,
				attempt+1)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.waits[attempt]):
		}
	}
}

func (s *Sender) post(ctx context.Context, msg message, sub orchestration.PushSubscription,
	private, public string,
) (int, error) {
	resp, err := webpush.SendNotificationWithContext(ctx, msg.payload, &webpush.Subscription{
		Endpoint: sub.Endpoint, Keys: webpush.Keys{Auth: sub.Auth, P256dh: sub.P256dh},
	}, &webpush.Options{
		HTTPClient: s.client, Subscriber: s.publicURL, VAPIDPublicKey: public, VAPIDPrivateKey: private,
		TTL: ttl, Urgency: webpush.UrgencyHigh, Topic: topic(msg.tag),
	})
	if err != nil {
		// A transport error quotes the endpoint, a capability URL: only its host is said.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}

		return 0, fmt.Errorf("pushing to %s: %w", host(sub.Endpoint), err)
	}

	defer resp.Body.Close() //nolint:errcheck // The body was read; nothing is left to report.

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))

	return resp.StatusCode, nil
}

func (s *Sender) fail(ctx context.Context, what string, err error) {
	if ctx.Err() != nil {
		return
	}

	s.logger.ErrorContext(ctx, what, slog.String("event", "webpush.failed"), slog.String("error", err.Error()))
}

// host is the endpoint's host, which logs and errors name instead of the endpoint.
func host(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "the push service"
	}

	return u.Host
}
