package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Web Push (issue #1047): what waits on the owner reaches his browsers and the iPhone Home Screen app when no tab is
// open. The owner alone uses the app (owner, 2026-11-21), so a subscription is a browser of his, with no user, as the
// owner's Second Bite keeps one (docs/specs/notification.md). An item that waits on him queues its push in its own
// transaction (push_outbox), and the sender (package webpush) reads the queue after the commit, as Second Bite's
// sender reads its outbox (server/internal/notification/push_sender.go).

// PushKind is what a push is about: the four kinds of what waits on the owner, phase 1 of issue #1047, from
// OpenClaw's approval and question categories (src/gateway/event-web-push.ts) and Chatwoot's per-item notifications.
type PushKind string

// The kinds of push.
const (
	PushQuestion          PushKind = "question"
	PushApproval          PushKind = "approval"
	PushConnectionRequest PushKind = "connection_request"
	PushTodo              PushKind = "todo"
)

// The bounds of a push's words. A push service takes 4 KiB of payload at most (RFC 8291 and Apple's limit); the
// item's own text is cut to fit with room for the rest, as OpenClaw's detailed level shows the item's text.
const (
	pushTitleBytes = 200
	pushBodyBytes  = 1500
	pushOutboxPage = 50
)

// PushSubscription is one browser of the owner's that takes pushes.
type PushSubscription struct {
	ID       string
	Endpoint string
	// P256dh and Auth are the browser's keys the payload is encrypted to (RFC 8291).
	P256dh    string
	Auth      string
	Label     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPushSubscription is a browser's subscription as it sends it.
type NewPushSubscription struct {
	Endpoint string
	P256dh   string
	Auth     string
	Label    string
}

// SavePushSubscription keeps a browser's subscription; the same browser again keeps its row and gets its new keys,
// as Coder's upsert on the endpoint does after a reinstall (coderd/database/queries/notifications.sql:198-209).
func (s *Service) SavePushSubscription(ctx context.Context, n NewPushSubscription) (PushSubscription, error) {
	n.Label = strings.TrimSpace(n.Label)

	switch {
	case !strings.HasPrefix(n.Endpoint, "https://") || len(n.Endpoint) > 2000:
		return PushSubscription{}, fmt.Errorf("%w: a push endpoint is an https URL of at most 2000 bytes", ErrInvalid)
	case n.P256dh == "" || len(n.P256dh) > 200 || n.Auth == "" || len(n.Auth) > 100:
		return PushSubscription{}, fmt.Errorf("%w: a subscription needs its p256dh and auth keys", ErrInvalid)
	case utf8.RuneCountInString(n.Label) > 100:
		return PushSubscription{}, fmt.Errorf("%w: a device's label is at most 100 characters", ErrInvalid)
	}

	var out PushSubscription

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.UpsertPushSubscription(ctx, store.UpsertPushSubscriptionParams{
			ID: s.newID("psb"), Endpoint: n.Endpoint, P256dh: n.P256dh, Auth: n.Auth, Label: n.Label, Now: now,
		})
		if err != nil {
			return fmt.Errorf("keeping the push subscription: %w", err)
		}

		out = pushSubscriptionFromRow(row)

		return nil
	})

	return out, err
}

// ListPushSubscriptions returns the owner's browsers that take pushes, the oldest first.
func (s *Service) ListPushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := s.read().ListPushSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the push subscriptions: %w", err)
	}

	out := make([]PushSubscription, 0, len(rows))
	for _, r := range rows {
		out = append(out, pushSubscriptionFromRow(r))
	}

	return out, nil
}

// GetPushSubscription returns one browser that takes pushes.
func (s *Service) GetPushSubscription(ctx context.Context, id string) (PushSubscription, error) {
	row, err := s.read().GetPushSubscription(ctx, id)
	if err != nil {
		return PushSubscription{}, notFound(err, "push subscription", id)
	}

	return pushSubscriptionFromRow(row), nil
}

// DeletePushSubscription stops pushes to one browser.
func (s *Service) DeletePushSubscription(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeletePushSubscription(ctx, id)
		if err != nil {
			return fmt.Errorf("deleting push subscription %s: %w", id, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: push subscription %s", ErrNotFound, id)
		}

		return nil
	})
}

// DeletePushSubscriptionByEndpoint forgets a browser the push service says is gone (404 or 410), as all four
// references do (Coder's coderd/webpush/webpush.go:54-63).
func (s *Service) DeletePushSubscriptionByEndpoint(ctx context.Context, endpoint string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if _, err := q.DeletePushSubscriptionByEndpoint(ctx, endpoint); err != nil {
			return fmt.Errorf("deleting a push subscription: %w", err)
		}

		return nil
	})
}

// ReplacePushKeys forgets every browser and keeps the new VAPID key with keep, in one transaction: a subscription
// made with the old public key takes no push signed with the new one, and when the key cannot be kept no browser is
// forgotten. As Coder's RegenerateVAPIDKeys deletes them and stores the keys in one transaction
// (coderd/webpush/webpush.go:534); keep is the vault's write (vault.PutStep), as a deleted chat's gateway keys are
// revoked in orchestration's transaction (WithChatKeys).
func (s *Service) ReplacePushKeys(ctx context.Context, keep func(ctx context.Context, tx *sql.Tx, now int64) error,
) (int, error) {
	var n int64

	err := s.writeTx(ctx, func(tx *sql.Tx, q *store.Queries, now int64) error {
		var err error

		n, err = q.DeleteAllPushSubscriptions(ctx)
		if err != nil {
			return fmt.Errorf("deleting the push subscriptions: %w", err)
		}

		return keep(ctx, tx, now)
	})
	if err != nil {
		return 0, err
	}

	return int(n), nil
}

func pushSubscriptionFromRow(r store.PushSubscription) PushSubscription {
	return PushSubscription{
		ID: r.ID, Endpoint: r.Endpoint, P256dh: r.P256dh, Auth: r.Auth, Label: r.Label,
		CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
	}
}

// queuePush queues the push of an item that waits on the owner, in the item's own transaction: the sender reads it
// only once the item is committed (Second Bite's outbox).
func queuePush(ctx context.Context, q *store.Queries, now int64, kind PushKind, id string) error {
	if err := q.InsertPushOutbox(ctx, store.InsertPushOutboxParams{Kind: string(kind), ItemID: id, Now: now}); err != nil {
		return fmt.Errorf("queuing the push of %s %s: %w", kind, id, err)
	}

	return nil
}

// PushNotice is one queued push with its words, read when it is sent.
type PushNotice struct {
	Seq    int64
	Kind   PushKind
	ItemID string
	// Waiting says the item still waits on the owner: one answered, decided or gone before its push is sent is not
	// pushed, as OpenClaw fences a push whose item ended before it was sent (src/gateway/event-web-push.ts,
	// mention.isCurrent).
	Waiting bool
	// Title says which Bot or Run asks; Body is the item's own text, cut to fit; Path the page that shows it.
	Title string
	Body  string
	Path  string
}

// QueuedPushes returns the pushes queued, the oldest first, with their words read now.
func (s *Service) QueuedPushes(ctx context.Context) ([]PushNotice, error) {
	rows, err := s.read().ListPushOutbox(ctx, pushOutboxPage)
	if err != nil {
		return nil, fmt.Errorf("reading the queued pushes: %w", err)
	}

	out := make([]PushNotice, 0, len(rows))

	for _, r := range rows {
		n, err := s.pushNotice(ctx, PushKind(r.Kind), r.ItemID)
		if err != nil {
			return nil, err
		}

		n.Seq, n.Kind, n.ItemID = r.Seq, PushKind(r.Kind), r.ItemID
		n.Title, n.Body = cutBytes(n.Title, pushTitleBytes), cutBytes(n.Body, pushBodyBytes)
		out = append(out, n)
	}

	return out, nil
}

// PushSent takes a queued push off the queue: it was sent, or it no longer had to be.
func (s *Service) PushSent(ctx context.Context, seq int64) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if err := q.DeletePushOutbox(ctx, seq); err != nil {
			return fmt.Errorf("taking push %d off the queue: %w", seq, err)
		}

		return nil
	})
}

// pushNotice reads an item's words. An item that is gone is a notice that no longer waits.
func (s *Service) pushNotice(ctx context.Context, kind PushKind, id string) (PushNotice, error) {
	var (
		n   PushNotice
		err error
	)

	switch kind {
	case PushQuestion:
		n, err = s.questionNotice(ctx, id)
	case PushApproval:
		n, err = s.approvalNotice(ctx, id)
	case PushConnectionRequest:
		n, err = s.connectionRequestNotice(ctx, id)
	case PushTodo:
		n, err = s.todoNotice(ctx, id)
	default:
		return PushNotice{}, nil
	}

	if errors.Is(err, ErrNotFound) {
		return PushNotice{}, nil
	}

	return n, err
}

func (s *Service) questionNotice(ctx context.Context, id string) (PushNotice, error) {
	question, err := s.GetQuestion(ctx, id)
	if err != nil {
		return PushNotice{}, err
	}

	who, path, err := s.runAsker(ctx, question.RunID)
	if err != nil {
		return PushNotice{}, err
	}

	return PushNotice{
		Waiting: question.Status == QuestionOpen && question.AddressedTo == AddressOwner,
		Title:   who + " asks", Body: question.Text, Path: path,
	}, nil
}

func (s *Service) approvalNotice(ctx context.Context, id string) (PushNotice, error) {
	approval, err := s.GetApproval(ctx, id)
	if err != nil {
		return PushNotice{}, err
	}

	who, path := "A Routine", "/"

	switch {
	case approval.ChatID != "":
		who, path, err = s.chatAsker(ctx, approval.ChatID)
	case approval.RunID != "":
		who, path, err = s.runAsker(ctx, approval.RunID)
	}

	if err != nil {
		return PushNotice{}, err
	}

	return PushNotice{
		Waiting: approval.Status == ApprovalPending,
		Title:   who + " waits for your approval", Body: approval.Title, Path: path,
	}, nil
}

func (s *Service) connectionRequestNotice(ctx context.Context, id string) (PushNotice, error) {
	req, err := s.GetConnectionRequest(ctx, id)
	if err != nil {
		return PushNotice{}, err
	}

	who, err := s.botName(ctx, req.Bot)
	if err != nil {
		return PushNotice{}, err
	}

	body := req.Use()
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		body += ": " + reason
	}

	return PushNotice{
		Waiting: req.State == RequestPending,
		Title:   who + " asks for a connection", Body: body, Path: "/chats/" + req.Chat,
	}, nil
}

func (s *Service) todoNotice(ctx context.Context, id string) (PushNotice, error) {
	todo, err := s.inbox.Get(ctx, id)
	if err != nil {
		return PushNotice{}, err
	}

	now := s.clock.Now()

	needs, err := s.read().TodoNeedsYou(ctx, store.TodoNeedsYouParams{
		ID: id, Now: now.UnixMilli(), StaleBefore: now.Add(-inboxstore.StaleAfter).UnixMilli(),
	})
	if err != nil {
		return PushNotice{}, fmt.Errorf("reading whether to-do %s needs the owner: %w", id, err)
	}

	who := "A Bot"
	if todo.CreatedBy.Kind == inboxstore.Bot {
		if who, err = s.botName(ctx, todo.CreatedBy.ID); err != nil {
			return PushNotice{}, err
		}
	}

	// The Inbox has no page in the web client yet (issue #838): the push opens the chat the item came from, when it
	// came from one.
	path := "/"
	if todo.Source.Kind == inboxstore.FromChat && todo.Source.ID != "" {
		path = "/chats/" + todo.Source.ID
	}

	return PushNotice{Waiting: needs > 0, Title: who + " added a to-do", Body: todo.Title, Path: path}, nil
}

// runAsker names a Run by its chat's Bot, or by its objective when it has no chat, and the page that shows its
// questions: its chat, else the start page.
func (s *Service) runAsker(ctx context.Context, runID string) (who, path string, err error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return "", "", err
	}

	if run.ChatID != "" {
		return s.chatAsker(ctx, run.ChatID)
	}

	return "Run “" + cutBytes(run.Objective, 80) + "”", "/", nil
}

// chatAsker names a chat by its Bot.
func (s *Service) chatAsker(ctx context.Context, chatID string) (who, path string, err error) {
	chat, err := s.GetChat(ctx, chatID)
	if err != nil {
		return "", "", err
	}

	who, err = s.botName(ctx, chat.Bot)

	return who, "/chats/" + chatID, err
}

func (s *Service) botName(ctx context.Context, id string) (string, error) {
	bot, err := s.GetBot(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return "A Bot", nil
	}

	if err != nil {
		return "", err
	}

	return bot.Name, nil
}

// cutBytes cuts a text to at most n bytes at a character's edge, with an ellipsis when it was cut.
func cutBytes(text string, n int) string {
	text = strings.TrimSpace(text)
	if len(text) <= n {
		return text
	}

	const ellipsis = "…"

	end := n - len(ellipsis)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}

	return strings.TrimSpace(text[:end]) + ellipsis
}
