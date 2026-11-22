// Package inboxstore keeps the Inbox (issues #838, #845): the to-dos that have to happen, whoever does them (the
// owner, a person he waits on, or a Bot), and the history of each. It is a domain store of the orchestration
// database reached through the root package's Inbox(), as Omnara's storage facade reaches its stores (issue #404);
// it imports the generated store and never the root package, writes only its own tables and the push queue
// (push_outbox, issue #1047), and reads the Bots and Approvals through the shared queries.
//
// A to-do's fields follow Northstar's Task and Omi's action item (models/action_item.py: who, due, typed
// evidence; models/task_due.py); its states Plane's Intake (db/models/intake.py: pending, accepted, rejected,
// snoozed, duplicate) and Hermes's kanban (running, review, done); its history MemoryOS's meeting_minutes_event.
// Every state moves only as the transitions in transitions.go allow, in one guarded write at the revision the
// caller read (docs/conventions.md, "Transitions are guarded writes"), with its event in the same transaction.
//
// The store trusts its caller for who acts: the API layer takes the Actor from the authenticated principal, never
// from a field the client sends.
package inboxstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // A due date's zone is an IANA name; the server may have no zoneinfo.
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The states of a to-do.
const (
	// Proposed: a Bot found it; it waits for the owner to accept, drop or mark it duplicate. Left unanswered it
	// stays proposed (owner, 2026-11-18).
	Proposed = "proposed"
	// Open: to be done.
	Open = "open"
	// Doing: a Bot is on it, in the attempt Work names.
	Doing = "doing"
	// Review: one of the Bot's actions waits for the owner's Approval.
	Review = "review"
	// Done, Dropped and Duplicate are closed; only the owner reopens them.
	Done      = "done"
	Dropped   = "dropped"
	Duplicate = "duplicate"
)

// Closed says whether a state is one of the closed ones.
func Closed(state string) bool { return state == Done || state == Dropped || state == Duplicate }

// The kinds of Actor.
const (
	Owner  = "owner"
	Bot    = "bot"
	System = "system"
)

// Actor is who makes a change: the owner, a Bot (ID its id) or the system (ID what of it, such as "approvals").
type Actor struct {
	Kind string
	ID   string
}

// The kinds of Due.
const (
	DueNone    = "none"
	DueUnknown = "unknown"
	DueDate    = "date"
	DueInstant = "instant"
)

// Due is when a to-do is due: none, unknown, a calendar date in the owner's time zone (no time is made up for it)
// or a moment with its time zone.
type Due struct {
	Kind string
	// Date is YYYY-MM-DD, for a date.
	Date string
	// At is the moment, for an instant.
	At time.Time
	// Zone is the IANA time zone of a date or an instant.
	Zone string
}

// The kinds of Assignee, and of who did a to-do.
const (
	Me      = "me"
	Person  = "person"
	ABot    = "bot"
	Unknown = "unknown"
)

// Assignee is who does a to-do, or who did it: me (the owner), a person by name, a Bot by its id, or unknown.
type Assignee struct {
	Kind string
	// Name is the person's name or the Bot's id.
	Name string
}

// The kinds of Source.
const (
	// ByHand: the owner wrote it.
	ByHand = "owner"
	// FromChat: a Bot in a chat with the owner; ID is the chat.
	FromChat = "chat"
	// FromRoutine: a Bot in a Routine's run; ID is the run.
	FromRoutine = "routine"
	// FromLark and FromZalo: a chat of the owner's own account at Lark or Zalo, the main sources (owner,
	// 2026-11-19); ID is the chat's id at the service and Ref the message, a LinkLarkMessage or LinkZaloMessage. A
	// Zalo source names its Account too: the Zalo store keys a message by account, chat and id
	// (messaging/zalo/store.go, zl_messages).
	FromLark = "lark"
	FromZalo = "zalo"
	// FromWhatsApp: a chat of the owner's own WhatsApp (issue #1004), named by its account and chat as Zalo's is
	// (messaging/whatsapp/store.go, wa_messages); Ref a LinkWhatsAppMessage.
	FromWhatsApp = "whatsapp"
)

// sourceMessage is the kind of Link a source at a messaging service names its message by.
var sourceMessage = map[string]string{
	FromLark: LinkLarkMessage, FromZalo: LinkZaloMessage, FromWhatsApp: LinkWhatsAppMessage,
}

// fromMessaging says whether a source is a chat of the owner's own account at a messaging service.
func fromMessaging(kind string) bool { return sourceMessage[kind] != "" }

// Source is where a to-do came from, with the words it came from and a typed reference to them.
type Source struct {
	Kind string
	ID   string
	// Account is the owner's Connection (its label) the chat is in: set for FromZalo and FromWhatsApp, whose stores
	// key a message by account, chat and id; optional for FromLark; empty for any other.
	Account string
	Quote   string
	Ref     *Link
}

// Why a to-do a Routine's run found starts open, not proposed (the owner's triage of 2026-11-20 on #838, issue
// #966): what is clear opens, what is inferred is proposed. Omi's extraction takes only clear action items and
// Hermes's meeting-action-items only commitments with an owner; splitting by clarity is the owner's decision.
const (
	// AskedDirectly: someone tagged or messaged the owner and he has not answered.
	AskedDirectly = "asked_directly"
	// Promised: the owner promised it himself ("để em check").
	Promised = "promised"
)

// StaleAfter is how long a proposal nobody touched stays in sight (issue #966): after it, it is stale, left out of
// List unless asked for and not counted by NeedsYou; it is neither accepted nor deleted, and its history stays.
// Touched is any event after created. A constant the owner may change later (owner, 2026-11-20).
const StaleAfter = 3 * 24 * time.Hour

// The kinds of Link, Omi's typed source references.
const (
	LinkURL         = "url"
	LinkChatMessage = "chat_message"
	LinkMail        = "mail"
	LinkRun         = "run"
	LinkPullRequest = "pull_request"
	LinkMeetingLine = "meeting_line"
	// LinkLarkMessage, LinkZaloMessage and LinkWhatsAppMessage are a message at Lark, Zalo or WhatsApp; Ref is its
	// id there.
	LinkLarkMessage     = "lark_message"
	LinkZaloMessage     = "zalo_message"
	LinkWhatsAppMessage = "whatsapp_message"
	// LinkZaloSent, LinkWhatsAppSent and LinkLarkSent are a message the owner sent himself in a chat of his own
	// account there (issue #1004): Account the Connection, Chat the chat and Ref the message's id. It is the
	// evidence a Bot gives when it records for the owner that he answered (Done), checked against what the server
	// keeps of that account or what its API returns (Sent).
	LinkZaloSent     = "zalo_sent"
	LinkWhatsAppSent = "whatsapp_sent"
	LinkLarkSent     = "lark_sent"
)

var linkKinds = []string{
	LinkURL, LinkChatMessage, LinkMail, LinkRun, LinkPullRequest, LinkMeetingLine, LinkLarkMessage, LinkZaloMessage,
	LinkWhatsAppMessage, LinkZaloSent, LinkWhatsAppSent, LinkLarkSent,
}

// sentService is the source kind, the messaging service, of a Link to a message the owner sent.
var sentService = map[string]string{LinkZaloSent: FromZalo, LinkWhatsAppSent: FromWhatsApp, LinkLarkSent: FromLark}

// Link is a typed reference: evidence that a to-do is done, or where it came from. Account and Chat are set for a
// message the owner sent (LinkZaloSent, LinkWhatsAppSent, LinkLarkSent) only.
type Link struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Label   string `json:"label,omitempty"`
	Account string `json:"account,omitempty"`
	Chat    string `json:"chat,omitempty"`
}

// SentMessage is a message of a chat of the owner's own account, as the server keeps it or its service's API
// returns it.
type SentMessage struct {
	Chat string
	At   time.Time
	// FromMe says the owner sent it himself.
	FromMe bool
}

// Sent reads a message of a chat of the owner's own account at a messaging service (FromZalo, FromWhatsApp,
// FromLark) by its Connection's label, the chat and the message's id: storeerr.ErrNotFound when there is none.
type Sent interface {
	SentMessage(ctx context.Context, service, account, chat, id string) (SentMessage, error)
}

// The kinds of Work.
const (
	WorkChat = "chat"
	WorkRun  = "run"
)

// Work is a Bot's current attempt at a to-do, set by Start: Attempt is its own id, which the Bot's writes carry
// (Change.Attempt) and an Approval it asks records (orchestration.NewApproval.AttemptID); Kind and ID are its chat
// or Run, where it works, which may hold attempts at other to-dos too; Bot is the Bot that started it and StartedAt
// when.
type Work struct {
	Attempt   string
	Kind      string
	ID        string
	Bot       string
	StartedAt time.Time
}

// How a to-do was closed: by hand or by a Bot. Judged by AI and by time are kept for later parts and refused now.
const (
	ClosedByHand = "hand"
	ClosedByBot  = "bot"
	ClosedByAI   = "ai"
	ClosedByTime = "time"
)

// Limits of what a to-do holds (issue #845, Data).
const (
	MaxTitle    = 300
	MaxNotes    = 20_000
	MaxDraft    = 20_000
	MaxDoneWhen = 1_000
	MaxName     = 100
	MaxQuote    = 2_000
	MaxReason   = 2_000
	MaxEvidence = 20
	// MaxRef and MaxLabel bound one Link, MaxKey an idempotency key and MaxID an id given by the caller.
	MaxRef   = 2_000
	MaxLabel = 300
	MaxKey   = 200
	MaxID    = 200
	// MaxPage is the most items one List returns; MaxEvents the most events Get returns, the latest.
	MaxPage   = 200
	MaxEvents = 500
)

// Todo is one to-do.
type Todo struct {
	ID       string
	Revision int
	Title    string
	Notes    string
	DoneWhen string
	// Draft is the reply a Bot drafted for the owner to send himself (issue #1004): a Bot never sends to a client or
	// a boss.
	Draft    string
	Due      Due
	Assignee Assignee
	State    string
	// SnoozedUntil hides it until then; only on a proposed or open item.
	SnoozedUntil *time.Time
	Source       Source
	CreatedBy    Actor
	// WhyOpen is AskedDirectly or Promised for an item a Routine's run opened; empty for any other.
	WhyOpen string
	// Stale says it is a proposal nobody touched for StaleAfter, when it was read.
	Stale bool
	// Work is the Bot's attempt while doing or review.
	Work *Work
	// ApprovalID is the one pending Approval while review.
	ApprovalID string
	// DoneBy is who did it, RecordedBy who recorded it.
	DoneBy      *Assignee
	RecordedBy  *Actor
	ClosedHow   string
	CloseReason string
	Evidence    []Link
	ClosedAt    *time.Time
	DuplicateOf string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Events is its history, oldest first; filled by Get only.
	Events []Event
}

// Event is one change of a to-do.
type Event struct {
	ID       int64
	At       time.Time
	Revision int
	Actor    Actor
	// Kind is created, edited, state, snoozed or linked_approval.
	Kind string
	// Before and After are JSON objects of the fields that changed.
	Before json.RawMessage
	After  json.RawMessage
}

// The kinds of Event.
const (
	EventCreated        = "created"
	EventEdited         = "edited"
	EventState          = "state"
	EventSnoozed        = "snoozed"
	EventLinkedApproval = "linked_approval"
)

// Store keeps the Inbox.
type Store struct {
	db    *sql.DB
	clock platform.Clock
	newID func(prefix string) string
	// signal wakes the orchestration's event readers after each commit.
	signal func()
	// sent reads the messages the owner sent in his own accounts' chats, the evidence that he answered; nil reads
	// none.
	sent Sent
}

// WithSent lets the store check a message the owner sent, given as evidence that he answered (issue #1004).
func (s *Store) WithSent(sent Sent) *Store {
	s.sent = sent

	return s
}

// New returns the Store over the orchestration database. signal is the root's, called after each commit as for
// every other write.
func New(db *sql.DB, clock platform.Clock, newID func(prefix string) string, signal func()) *Store {
	return &Store{db: db, clock: clock, newID: newID, signal: signal}
}

func (s *Store) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return storeutil.Write(ctx, s.db, s.clock, s.signal, fn)
}

// snapshot runs fn over one transaction, so what it reads in several queries is one state of the database (an item
// and its history, a page and its total). The transaction writes nothing and is rolled back.
func (s *Store) snapshot(ctx context.Context, fn func(q *store.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a read: %w", err)
	}

	return errors.Join(fn(store.New(tx)), tx.Rollback())
}

// NewTodo is what Create makes.
type NewTodo struct {
	Title    string
	Notes    string
	DoneWhen string
	// Draft is left out of the request's hash when empty, so a key used before drafts existed (issue #1004) still
	// returns its item.
	Draft string `json:",omitempty"`
	Due   Due
	// DueIn, with no Due, makes the item due a while after it is first added: the day to chase a person (issue
	// #1004, section 6). The moment is taken when the item is made, so a retry with the same key is the same request.
	DueIn    *DueIn `json:",omitempty"`
	Assignee Assignee
	// State is Open or Proposed.
	State  string
	Source Source
	// WhyOpen is AskedDirectly or Promised when a Bot in a Routine's run, or reading the owner's Lark or Zalo
	// chats, opens the item; it then gives the quote and the reference to the message too.
	WhyOpen string
	// IdempotencyKey, when given, makes a retried create return the first item.
	IdempotencyKey string
}

// DueIn is a due a while after an item is added, in a time zone.
type DueIn struct {
	After time.Duration
	Zone  string
}

// Create adds a to-do. The owner adds open items by hand; a Bot adds open items in a chat with the owner (Source
// FromChat) and proposed ones in a Routine's run or from the owner's Lark or Zalo chats (Source FromRoutine,
// FromLark, FromZalo), or open ones there with WhyOpen, a quote and a reference. A create retried with the same
// IdempotencyKey and the same request returns the first item and writes nothing; with another request it is a
// conflict (docs/conventions.md, "Idempotency").
func (s *Store) Create(ctx context.Context, actor Actor, n NewTodo) (Todo, error) {
	n.Title = strings.TrimSpace(n.Title)
	n.Assignee.Name = strings.TrimSpace(n.Assignee.Name)

	if err := checkActor(actor); err != nil {
		return Todo{}, err
	}

	if err := mayCreate(actor, n); err != nil {
		return Todo{}, err
	}

	if err := checkText(n.Title, n.Notes, n.DoneWhen); err != nil {
		return Todo{}, err
	}

	if err := checkDraft(n.Draft); err != nil {
		return Todo{}, err
	}

	due, err := dueRow(n.Due)
	if err != nil {
		return Todo{}, err
	}

	if n.DueIn != nil {
		_, zoneErr := time.LoadLocation(n.DueIn.Zone)

		switch {
		case due.kind != DueNone || n.DueIn.After <= 0:
			return Todo{}, fmt.Errorf("%w: a due a while after the item is added replaces a due, and is later",
				storeerr.ErrInvalid)
		case zoneErr != nil || n.DueIn.Zone == "" || n.DueIn.Zone == "Local":
			return Todo{}, fmt.Errorf("%w: %q is not an IANA time zone", storeerr.ErrInvalid, n.DueIn.Zone)
		}
	}

	if err := checkSource(n.Source); err != nil {
		return Todo{}, err
	}

	if utf8.RuneCountInString(n.IdempotencyKey) > MaxKey {
		return Todo{}, fmt.Errorf("%w: an idempotency key is at most %d characters", storeerr.ErrInvalid, MaxKey)
	}

	ref, err := optionalJSON(n.Source.Ref)
	if err != nil {
		return Todo{}, err
	}

	hash, err := requestHash(actor, n)
	if err != nil {
		return Todo{}, err
	}

	var made Todo

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if n.IdempotencyKey != "" {
			first, err := q.GetTodoByIdempotencyKey(ctx, nullString(n.IdempotencyKey))

			switch {
			case err == nil && first.IdempotencyHash.String == hash:
				made, err = s.get(ctx, q, first.ID)

				return err
			case err == nil:
				return fmt.Errorf("%w: the idempotency key %q was used for another request", storeerr.ErrConflict,
					n.IdempotencyKey)
			case !errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("reading the to-do of the idempotency key: %w", err)
			}
		}

		if err := checkAssignee(ctx, q, n.Assignee); err != nil {
			return err
		}

		if n.DueIn != nil {
			if due, err = dueRow(Due{Kind: DueInstant, At: time.UnixMilli(now).Add(n.DueIn.After),
				Zone: n.DueIn.Zone}); err != nil {
				return err
			}
		}

		id := s.newID("tdo")
		if err := q.InsertTodo(ctx, store.InsertTodoParams{
			ID: id, Title: n.Title, Notes: n.Notes, DoneWhen: n.DoneWhen, Draft: n.Draft,
			DueKind: due.kind, DueDate: due.date, DueZone: due.zone, DueAt: due.at, OverdueAt: due.overdueAt,
			AssigneeKind: n.Assignee.Kind, Assignee: n.Assignee.Name, State: n.State,
			SourceKind: n.Source.Kind, SourceID: n.Source.ID, SourceAccount: n.Source.Account,
			SourceQuote: n.Source.Quote, SourceRef: ref,
			CreatedByKind: actor.Kind, CreatedBy: actor.ID, WhyOpen: nullString(n.WhyOpen),
			IdempotencyKey: nullString(n.IdempotencyKey), IdempotencyHash: nullString(keyed(n.IdempotencyKey, hash)),
			Now: now,
		}); err != nil {
			return fmt.Errorf("adding the to-do: %w", err)
		}

		row, err := q.GetTodo(ctx, id)
		if err != nil {
			return fmt.Errorf("reading the to-do added: %w", err)
		}

		if err := addEvent(ctx, q, row.ID, now, 1, actor, EventCreated, map[string]any{}, fields(row)); err != nil {
			return err
		}

		if err := queuePush(ctx, q, now, actor, id); err != nil {
			return err
		}

		made, err = s.get(ctx, q, id)

		return err
	})
	if err != nil {
		return Todo{}, err
	}

	return made, nil
}

// mayCreate is the creation rows of the transitions (issue #845) with the owner's triage of 2026-11-20 (issue
// #966): the owner adds open items by hand; a Bot adds open items in a chat with the owner; in a Routine's run or
// from the owner's Lark or Zalo chats it adds proposed ones (Plane's intake: what is found waits as pending), or open
// ones when it says why (WhyOpen) and gives the quote and the reference to the message.
func mayCreate(actor Actor, n NewTodo) error {
	state, source := n.State, n.Source.Kind
	found := source == FromRoutine || fromMessaging(source)

	switch {
	case state != Open && state != Proposed:
		return fmt.Errorf("%w: a to-do starts open or proposed", storeerr.ErrInvalid)
	case !slices.Contains([]string{ByHand, FromChat, FromRoutine, FromLark, FromZalo, FromWhatsApp}, source):
		return fmt.Errorf("%w: unknown source %q", storeerr.ErrInvalid, source)
	case n.WhyOpen != "" && n.WhyOpen != AskedDirectly && n.WhyOpen != Promised:
		return fmt.Errorf("%w: why a to-do opens is %s or %s, not %q", storeerr.ErrInvalid, AskedDirectly, Promised,
			n.WhyOpen)
	case n.WhyOpen != "" && (state != Open || !found):
		return fmt.Errorf("%w: only an open to-do a Routine's run found says why it opens", storeerr.ErrInvalid)
	case actor.Kind == Owner && state == Open && source == ByHand:
		return nil
	case actor.Kind == Bot && state == Open && source == FromChat:
		return nil
	case actor.Kind == Bot && state == Proposed && found:
		return nil
	case actor.Kind == Bot && state == Open && found:
		if n.WhyOpen == "" || strings.TrimSpace(n.Source.Quote) == "" || n.Source.Ref == nil {
			return fmt.Errorf("%w: a Bot opens a to-do it found only when it was asked directly or promised, with "+
				"the quote and the reference to the message; else it proposes it", storeerr.ErrForbidden)
		}

		return nil
	}

	return fmt.Errorf("%w: a %s cannot add a %s to-do from %s", storeerr.ErrForbidden, actor.Kind, state, source)
}

func checkActor(a Actor) error {
	switch {
	case a.Kind == Bot && a.ID == "":
		return fmt.Errorf("%w: a Bot acts by its id", storeerr.ErrInvalid)
	case a.Kind == Owner || a.Kind == Bot || a.Kind == System:
		return nil
	}

	return fmt.Errorf("%w: unknown actor %q", storeerr.ErrInvalid, a.Kind)
}

func checkText(title, notes, doneWhen string) error {
	switch {
	case title == "" || utf8.RuneCountInString(title) > MaxTitle:
		return fmt.Errorf("%w: a title is 1 to %d characters", storeerr.ErrInvalid, MaxTitle)
	case utf8.RuneCountInString(notes) > MaxNotes:
		return fmt.Errorf("%w: notes are at most %d characters", storeerr.ErrInvalid, MaxNotes)
	case utf8.RuneCountInString(doneWhen) > MaxDoneWhen:
		return fmt.Errorf("%w: done-when is at most %d characters", storeerr.ErrInvalid, MaxDoneWhen)
	}

	return nil
}

func checkSource(src Source) error {
	switch {
	case src.Kind == ByHand && src.ID != "":
		return fmt.Errorf("%w: a to-do written by hand names no chat or run", storeerr.ErrInvalid)
	case src.Kind != ByHand && (src.ID == "" || len(src.ID) > MaxID):
		return fmt.Errorf("%w: a to-do from a %s names it", storeerr.ErrInvalid, src.Kind)
	case (src.Kind == FromZalo || src.Kind == FromWhatsApp) && src.Account == "":
		return fmt.Errorf("%w: a to-do from %s names the owner's account the chat is in", storeerr.ErrInvalid, src.Kind)
	case len(src.Account) > MaxID:
		return fmt.Errorf("%w: an account is at most %d characters", storeerr.ErrInvalid, MaxID)
	case !fromMessaging(src.Kind) && src.Account != "":
		return fmt.Errorf("%w: only a to-do from Lark, Zalo or WhatsApp names an account", storeerr.ErrInvalid)
	case utf8.RuneCountInString(src.Quote) > MaxQuote:
		return fmt.Errorf("%w: a quote is at most %d characters", storeerr.ErrInvalid, MaxQuote)
	case sourceMessage[src.Kind] != "" && (src.Ref == nil || src.Ref.Kind != sourceMessage[src.Kind]):
		return fmt.Errorf("%w: a to-do from %s names its message as a %s", storeerr.ErrInvalid, src.Kind,
			sourceMessage[src.Kind])
	case src.Ref != nil:
		return checkLink(*src.Ref)
	}

	return nil
}

func checkLink(l Link) error {
	switch {
	case !slices.Contains(linkKinds, l.Kind):
		return fmt.Errorf("%w: a link is one of %s, not %q", storeerr.ErrInvalid, strings.Join(linkKinds, ", "),
			l.Kind)
	case l.Ref == "" || utf8.RuneCountInString(l.Ref) > MaxRef:
		return fmt.Errorf("%w: a link's reference is 1 to %d characters", storeerr.ErrInvalid, MaxRef)
	case utf8.RuneCountInString(l.Label) > MaxLabel:
		return fmt.Errorf("%w: a link's label is at most %d characters", storeerr.ErrInvalid, MaxLabel)
	case sentService[l.Kind] != "" && (l.Account == "" || l.Chat == "" || len(l.Account) > MaxID ||
		len(l.Chat) > MaxID):
		return fmt.Errorf("%w: a %s names the owner's account and the chat it was sent in", storeerr.ErrInvalid, l.Kind)
	case sentService[l.Kind] == "" && (l.Account != "" || l.Chat != ""):
		return fmt.Errorf("%w: only a message the owner sent names an account and a chat", storeerr.ErrInvalid)
	}

	return nil
}

func checkDraft(draft string) error {
	if utf8.RuneCountInString(draft) > MaxDraft {
		return fmt.Errorf("%w: a draft is at most %d characters", storeerr.ErrInvalid, MaxDraft)
	}

	return nil
}

func checkEvidence(links []Link) error {
	if len(links) > MaxEvidence {
		return fmt.Errorf("%w: at most %d pieces of evidence", storeerr.ErrInvalid, MaxEvidence)
	}

	for _, l := range links {
		if err := checkLink(l); err != nil {
			return err
		}
	}

	return nil
}

// checkAssignee checks who does it; a Bot must exist.
func checkAssignee(ctx context.Context, q *store.Queries, a Assignee) error {
	switch a.Kind {
	case Me, Unknown:
		if a.Name != "" {
			return fmt.Errorf("%w: %s names no one", storeerr.ErrInvalid, a.Kind)
		}

		return nil
	case Person:
		if a.Name == "" || utf8.RuneCountInString(a.Name) > MaxName {
			return fmt.Errorf("%w: a person's name is 1 to %d characters", storeerr.ErrInvalid, MaxName)
		}

		return nil
	case ABot:
		if _, err := q.GetBot(ctx, a.Name); err != nil {
			return storeutil.NotFound(err, "Bot", a.Name)
		}

		return nil
	}

	return fmt.Errorf("%w: unknown assignee %q", storeerr.ErrInvalid, a.Kind)
}

// dueColumns is a Due as stored.
type dueColumns struct {
	kind          string
	date, zone    sql.NullString
	at, overdueAt sql.NullInt64
}

// dueRow checks a Due and gives its columns. A date's first moment in its zone orders it and the next day's makes it
// overdue; neither is shown as a time.
func dueRow(d Due) (dueColumns, error) {
	switch d.Kind {
	case DueNone, DueUnknown, "":
		if d.Date != "" || !d.At.IsZero() || d.Zone != "" {
			return dueColumns{}, fmt.Errorf("%w: a to-do without a due date has no date, time or zone",
				storeerr.ErrInvalid)
		}

		kind := d.Kind
		if kind == "" {
			kind = DueNone
		}

		return dueColumns{kind: kind}, nil
	case DueDate, DueInstant:
	default:
		return dueColumns{}, fmt.Errorf("%w: unknown due %q", storeerr.ErrInvalid, d.Kind)
	}

	loc, err := time.LoadLocation(d.Zone)
	if err != nil || d.Zone == "" || d.Zone == "Local" {
		return dueColumns{}, fmt.Errorf("%w: %q is not an IANA time zone", storeerr.ErrInvalid, d.Zone)
	}

	if d.Kind == DueInstant {
		if d.At.IsZero() || d.Date != "" {
			return dueColumns{}, fmt.Errorf("%w: a due moment has its time and no date", storeerr.ErrInvalid)
		}

		at := sql.NullInt64{Int64: d.At.UnixMilli(), Valid: true}

		return dueColumns{kind: DueInstant, zone: nullString(d.Zone), at: at, overdueAt: at}, nil
	}

	// The date's own fields, never those of a moment the zone shifted: where midnight is skipped, time.Date puts it
	// on the evening before.
	day, err := time.Parse(time.DateOnly, d.Date)
	if err != nil || !d.At.IsZero() {
		return dueColumns{}, fmt.Errorf("%w: a due date is YYYY-MM-DD, without a time", storeerr.ErrInvalid)
	}

	start, next := firstMoment(day, loc), firstMoment(day.AddDate(0, 0, 1), loc)

	return dueColumns{
		kind: DueDate, date: nullString(d.Date), zone: nullString(d.Zone),
		at:        sql.NullInt64{Int64: start.UnixMilli(), Valid: true},
		overdueAt: sql.NullInt64{Int64: next.UnixMilli(), Valid: true},
	}, nil
}

// firstMoment is the first moment in loc of the calendar day day (its fields, in UTC): its midnight or, where the
// clocks skip midnight (America/Santiago on 2026-09-06 goes from 23:59:59 to 01:00), the moment they change.
func firstMoment(day time.Time, loc *time.Location) time.Time {
	y, m, d := day.Date()
	t := time.Date(y, m, d, 0, 0, 0, 0, loc)

	if t.Day() != d {
		// Midnight does not exist that day and t fell on the evening before: the day starts when its zone ends.
		_, end := t.ZoneBounds()
		if !end.IsZero() {
			return end
		}
	}

	return t
}

// requestHash is the hash of a create's request and who made it, kept with its idempotency key.
func requestHash(actor Actor, n NewTodo) (string, error) {
	n.IdempotencyKey = ""

	b, err := json.Marshal(struct {
		Actor Actor
		New   NewTodo
	}{actor, n})
	if err != nil {
		return "", fmt.Errorf("hashing the request: %w", err)
	}

	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:]), nil
}

func keyed(key, hash string) string {
	if key == "" {
		return ""
	}

	return hash
}

// Get returns a to-do with its latest MaxEvents events, oldest first, read together: the history ends at the item's
// revision.
func (s *Store) Get(ctx context.Context, id string) (Todo, error) {
	var t Todo

	err := s.snapshot(ctx, func(q *store.Queries) error {
		var err error
		t, err = s.get(ctx, q, id)

		return err
	})

	return t, err
}

func (s *Store) get(ctx context.Context, q *store.Queries, id string) (Todo, error) {
	row, err := q.GetTodo(ctx, id)
	if err != nil {
		return Todo{}, storeutil.NotFound(err, "to-do", id)
	}

	t, err := fromRow(row, s.clock.Now())
	if err != nil {
		return Todo{}, err
	}

	rows, err := q.ListTodoEvents(ctx, store.ListTodoEventsParams{TodoID: id, Lim: MaxEvents})
	if err != nil {
		return Todo{}, fmt.Errorf("reading the history of to-do %s: %w", id, err)
	}

	t.Events = make([]Event, 0, len(rows))
	for _, r := range slices.Backward(rows) {
		t.Events = append(t.Events, Event{
			ID: r.ID, At: storeutil.Millis(r.At), Revision: int(r.Revision), Actor: Actor{Kind: r.ActorKind, ID: r.Actor},
			Kind: r.Kind, Before: json.RawMessage(r.Before), After: json.RawMessage(r.After),
		})
	}

	return t, nil
}

// Filter is which to-dos List returns and counts.
type Filter struct {
	// States are the states listed; none means every state that is not closed, or every state WithClosed.
	States     []string
	WithClosed bool
	// Assignee, when given, lists only the items of that assignee.
	Assignee *Assignee
	// SourceKind, when given, lists only the items from that kind of source.
	SourceKind string
	// DueFrom and DueTo, when given, list only items due in [DueFrom, DueTo); a date is due from its first moment.
	DueFrom, DueTo time.Time
	// WithSnoozed lists snoozed items too.
	WithSnoozed bool
	// NeedsYou lists only the items NeedsYou counts.
	NeedsYou bool
	// WithStale lists the stale proposals too: proposed, untouched for StaleAfter.
	WithStale bool
}

// Page is one page of List.
type Page struct {
	Items []Todo
	// Total is how many items match the filter, every page together.
	Total int
	// Next is the cursor of the next page; empty on the last.
	Next string
}

// List returns one page of the to-dos the filter matches, by due (those without one last), then created, after the
// cursor of the previous page; limit is 1 to MaxPage, 0 for MaxPage.
func (s *Store) List(ctx context.Context, f Filter, cursor string, limit int) (Page, error) {
	if limit == 0 {
		limit = MaxPage
	}

	if limit < 0 || limit > MaxPage {
		return Page{}, fmt.Errorf("%w: a page holds 1 to %d to-dos", storeerr.ErrInvalid, MaxPage)
	}

	cnt, err := s.filterParams(f)
	if err != nil {
		return Page{}, err
	}

	p := store.ListTodosParams{
		AnyState: cnt.AnyState, States: cnt.States, AssigneeKind: cnt.AssigneeKind, Assignee: cnt.Assignee,
		SourceKind: cnt.SourceKind, HasDueFrom: cnt.HasDueFrom, DueFrom: cnt.DueFrom, HasDueTo: cnt.HasDueTo,
		DueTo: cnt.DueTo, WithSnoozed: cnt.WithSnoozed, WithStale: cnt.WithStale, StaleBefore: cnt.StaleBefore,
		Now: cnt.Now, NeedsYou: cnt.NeedsYou, Lim: int64(limit) + 1,
	}

	if cursor != "" {
		p.HasCursor = 1
		if p.CursorDue, p.CursorAt, p.CursorID, err = decodeCursor(cursor); err != nil {
			return Page{}, err
		}
	}

	var (
		rows  []store.Todo
		total int64
	)

	// The page and its total are read together, so the total counts the items the page shows.
	err = s.snapshot(ctx, func(q *store.Queries) error {
		if rows, err = q.ListTodos(ctx, p); err != nil {
			return fmt.Errorf("listing the to-dos: %w", err)
		}

		if total, err = q.CountTodos(ctx, cnt); err != nil {
			return fmt.Errorf("counting the to-dos: %w", err)
		}

		return nil
	})
	if err != nil {
		return Page{}, err
	}

	page := Page{Items: make([]Todo, 0, min(len(rows), limit)), Total: int(total)}
	now := storeutil.Millis(cnt.Now)

	for i, row := range rows {
		if i == limit {
			last := rows[limit-1]
			page.Next = encodeCursor(last)

			break
		}

		t, err := fromRow(row, now)
		if err != nil {
			return Page{}, err
		}

		page.Items = append(page.Items, t)
	}

	return page, nil
}

// NeedsYou is how many distinct to-dos need the owner at now: not closed, not snoozed, and proposed but not stale,
// or in review with a pending Approval, or his own and overdue, or his own with a reply drafted for him to send
// (issue #1004). List's NeedsYou filter is the same predicate.
func (s *Store) NeedsYou(ctx context.Context, now time.Time) (int, error) {
	n, err := store.New(s.db).CountTodos(ctx, store.CountTodosParams{
		AnyState: 1, Now: now.UnixMilli(), NeedsYou: 1, StaleBefore: now.Add(-StaleAfter).UnixMilli(),
	})
	if err != nil {
		return 0, fmt.Errorf("counting the to-dos that need the owner: %w", err)
	}

	return int(n), nil
}

func (s *Store) filterParams(f Filter) (store.CountTodosParams, error) {
	now := s.clock.Now()
	p := store.CountTodosParams{Now: now.UnixMilli(), States: "[]", StaleBefore: now.Add(-StaleAfter).UnixMilli()}

	states := f.States
	if len(states) == 0 && !f.WithClosed {
		states = []string{Proposed, Open, Doing, Review}
	}

	if len(states) == 0 {
		p.AnyState = 1
	}

	for _, st := range states {
		if !slices.Contains([]string{Proposed, Open, Doing, Review, Done, Dropped, Duplicate}, st) {
			return p, fmt.Errorf("%w: unknown state %q", storeerr.ErrInvalid, st)
		}
	}

	b, err := json.Marshal(states)
	if err != nil {
		return p, fmt.Errorf("listing the states: %w", err)
	}

	p.States = string(b)

	if f.Assignee != nil {
		if !slices.Contains([]string{Me, Person, ABot, Unknown}, f.Assignee.Kind) {
			return p, fmt.Errorf("%w: unknown assignee %q", storeerr.ErrInvalid, f.Assignee.Kind)
		}

		p.AssigneeKind, p.Assignee = f.Assignee.Kind, f.Assignee.Name
	}

	p.SourceKind = f.SourceKind

	// A bound is given when it is not the zero time; the Unix epoch is a bound like any other.
	if !f.DueFrom.IsZero() {
		p.HasDueFrom, p.DueFrom = 1, f.DueFrom.UnixMilli()
	}

	if !f.DueTo.IsZero() {
		p.HasDueTo, p.DueTo = 1, f.DueTo.UnixMilli()
	}

	if f.WithSnoozed {
		p.WithSnoozed = 1
	}

	if f.NeedsYou {
		p.NeedsYou = 1
	}

	if f.WithStale {
		p.WithStale = 1
	}

	return p, nil
}

// noDue orders the items without a due after every other, as the queries' coalesce does.
const noDue = int64(9223372036854775807)

func encodeCursor(r store.Todo) string {
	due := noDue
	if r.DueAt.Valid {
		due = r.DueAt.Int64
	}

	raw := strconv.FormatInt(due, 10) + "|" + strconv.FormatInt(r.CreatedAt, 10) + "|" + r.ID

	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(c string) (due, at int64, id string, err error) {
	bad := fmt.Errorf("%w: the cursor is not one List gave", storeerr.ErrInvalid)

	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, 0, "", bad
	}

	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return 0, 0, "", bad
	}

	due, err1 := strconv.ParseInt(parts[0], 10, 64)
	at, err2 := strconv.ParseInt(parts[1], 10, 64)

	if err1 != nil || err2 != nil || parts[2] == "" {
		return 0, 0, "", bad
	}

	return due, at, parts[2], nil
}

// stale is the predicate of the queries' stale proposal: proposed, at revision 1 (no event after created) and made
// StaleAfter or longer before now.
func stale(r store.Todo, now time.Time) bool {
	return r.State == Proposed && r.Revision == 1 && r.CreatedAt <= now.Add(-StaleAfter).UnixMilli()
}

// fromRow is a stored to-do, read at now.
func fromRow(r store.Todo, now time.Time) (Todo, error) {
	t := Todo{
		ID: r.ID, Revision: int(r.Revision), Title: r.Title, Notes: r.Notes, DoneWhen: r.DoneWhen, Draft: r.Draft,
		Due:          Due{Kind: r.DueKind, Date: r.DueDate.String, Zone: r.DueZone.String},
		Assignee:     Assignee{Kind: r.AssigneeKind, Name: r.Assignee},
		State:        r.State,
		SnoozedUntil: storeutil.OptionalMillis(r.SnoozedUntil),
		Source:       Source{Kind: r.SourceKind, ID: r.SourceID, Account: r.SourceAccount, Quote: r.SourceQuote},
		CreatedBy:    Actor{Kind: r.CreatedByKind, ID: r.CreatedBy}, WhyOpen: r.WhyOpen.String, Stale: stale(r, now),
		ApprovalID: r.ApprovalID.String, ClosedHow: r.ClosedHow.String, CloseReason: r.CloseReason,
		ClosedAt: storeutil.OptionalMillis(r.ClosedAt), DuplicateOf: r.DuplicateOf.String,
		CreatedAt: storeutil.Millis(r.CreatedAt), UpdatedAt: storeutil.Millis(r.UpdatedAt),
	}

	if r.DueKind == DueInstant {
		t.Due.At = storeutil.Millis(r.DueAt.Int64)
	}

	if r.WorkKind.Valid {
		t.Work = &Work{Attempt: r.WorkAttempt.String, Kind: r.WorkKind.String, ID: r.WorkID.String, Bot: r.WorkBot.String,
			StartedAt: storeutil.Millis(r.WorkStartedAt.Int64)}
	}

	if r.DoneByKind.Valid {
		t.DoneBy = &Assignee{Kind: r.DoneByKind.String, Name: r.DoneBy.String}
	}

	if r.RecordedByKind.Valid {
		t.RecordedBy = &Actor{Kind: r.RecordedByKind.String, ID: r.RecordedBy.String}
	}

	if r.SourceRef.Valid {
		var ref Link
		if err := json.Unmarshal([]byte(r.SourceRef.String), &ref); err != nil {
			return Todo{}, fmt.Errorf("reading the source of to-do %s: %w", r.ID, err)
		}

		t.Source.Ref = &ref
	}

	if err := json.Unmarshal([]byte(r.Evidence), &t.Evidence); err != nil {
		return Todo{}, fmt.Errorf("reading the evidence of to-do %s: %w", r.ID, err)
	}

	return t, nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func optionalJSON(v *Link) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}

	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("writing the link: %w", err)
	}

	return sql.NullString{String: string(b), Valid: true}, nil
}

// queuePush queues the push of a to-do a Bot or the system added that needs the owner (issue #1047), in the
// transaction that adds it, as the root package queues an Approval's: one the owner wrote himself needs no push. The
// sender reads the queue (push_outbox) after the commit; "todo" is the root package's PushTodo.
func queuePush(ctx context.Context, q *store.Queries, now int64, actor Actor, id string) error {
	if actor.Kind == Owner {
		return nil
	}

	needs, err := q.TodoNeedsYou(ctx, store.TodoNeedsYouParams{
		ID: id, Now: now, StaleBefore: time.UnixMilli(now).Add(-StaleAfter).UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("reading whether to-do %s needs the owner: %w", id, err)
	}

	if needs == 0 {
		return nil
	}

	if err := q.InsertPushOutbox(ctx, store.InsertPushOutboxParams{Kind: "todo", ItemID: id, Now: now}); err != nil {
		return fmt.Errorf("queuing the push of to-do %s: %w", id, err)
	}

	return nil
}
