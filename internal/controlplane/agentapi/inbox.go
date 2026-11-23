package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The Inbox's tools (issue #1004, part 3 of #838): a Bot's pi keeps the owner's to-dos with them, one tool for each
// thing it does, as Orca's bot CLI has `item add / set / done / cancel` (src/main/bots/bot-preamble.ts:47) and
// Hermes's kanban its tools. They do what the client API lets a pi's token do (clientapi/inbox.go, #966), through
// the same store: who acts comes from the pi's token, never from an argument: the Bot, in its chat with the owner,
// or in the Routine's run its chat was opened for while the run is live (orchestration.InboxWorkOf). The store says
// what each may do (inboxstore's transitions); a Bot adds, edits and closes its own items without asking the owner
// (owner, 2026-11-20: "bắt duyệt vừa thôi").
//
// The Bot never handles the item's revision or attempt: each call reads the item and writes at the revision it
// read, a later change meanwhile being a conflict it is told, and carries the item's attempt when the Bot is doing
// it here. A worker is never given them, as it is not given routines: the owner's to-dos are his Bots'.

// inboxDefaultChase is how long after it is added an item given to someone else is due, when the Bot gives no due:
// the day to chase them (issue #1004, section 6; the 2 days are inference, owner-approved).
const inboxDefaultChase = 48 * time.Hour

// inboxEventsShown bounds the history inbox_get shows, the latest.
const inboxEventsShown = 20

// keepsInbox says whether the agent is given the Inbox's tools: a Bot's pi in its chat, and no other.
func keepsInbox(a agent) bool { return makesRoutines(a) }

// inboxCaller is who a pi's call acts as in the Inbox and where: the store's Actor, and the chat or the Routine's
// run its work is in.
type inboxCaller struct {
	actor inboxstore.Actor
	work  inboxstore.Work
}

// chat is the chat with the owner the Bot acts in, where his message is evidence; empty in a Routine's run.
func (c inboxCaller) chat() string {
	if c.work.Kind == inboxstore.WorkChat {
		return c.work.ID
	}

	return ""
}

func (s *Server) inboxCallerOf(ctx context.Context, a agent) (inboxCaller, error) {
	w, err := s.store.InboxWorkOf(ctx, a.chatID)
	if err != nil {
		return inboxCaller{}, err
	}

	return inboxCaller{actor: inboxstore.Actor{Kind: inboxstore.Bot, ID: a.bot}, work: w}, nil
}

// inboxLinkSchema is a typed reference, a source's message or a piece of evidence.
func inboxLinkSchema(about string) map[string]any {
	return map[string]any{
		"type": "object", "description": about, "required": []string{"kind", "ref"},
		"properties": map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{
				inboxstore.LinkURL, inboxstore.LinkChatMessage, inboxstore.LinkMail, inboxstore.LinkRun,
				inboxstore.LinkPullRequest, inboxstore.LinkMeetingLine, inboxstore.LinkLarkMessage,
				inboxstore.LinkZaloMessage, inboxstore.LinkWhatsAppMessage, inboxstore.LinkZaloSent,
				inboxstore.LinkWhatsAppSent, inboxstore.LinkLarkSent,
			}},
			"ref": map[string]any{"type": "string", "description": "The URL, or the id of the message, mail, Run, " +
				"pull request or meeting line; a message of this chat by its seq."},
			"label": map[string]any{"type": "string"},
			"account": map[string]any{"type": "string", "description": "zalo_sent, whatsapp_sent, lark_sent: the " +
				"owner's account (as the zalo_*, whatsapp_* tools name it, or his Lark Connection) it was sent from."},
			"chat": map[string]any{"type": "string", "description": "zalo_sent, whatsapp_sent, lark_sent: the " +
				"chat's id at the service."},
		},
	}
}

// inboxTools are the Inbox's tools as a Bot's pi is offered them.
func inboxTools(a agent) []agenttool.BrowserTool {
	if !keepsInbox(a) {
		return nil
	}

	id := map[string]any{"type": "string", "description": "The item's id, tdo_…."}
	revision := map[string]any{"type": "integer", "minimum": 1,
		"description": "The revision you read; left out, the item as it is now."}
	reason := map[string]any{"type": "string", "description": "Why, in a sentence."}
	evidence := map[string]any{"type": "array", "maxItems": inboxstore.MaxEvidence,
		"items": inboxLinkSchema("What shows it.")}
	due := map[string]any{
		"due_date": map[string]any{"type": "string", "description": "A calendar day, YYYY-MM-DD, in timezone."},
		"due_at":   map[string]any{"type": "string", "description": "A moment, RFC 3339, instead of due_date."},
		"timezone": map[string]any{"type": "string", "description": "The owner's IANA time zone of the due, " +
			"such as Asia/Ho_Chi_Minh."},
	}
	fields := map[string]any{
		"title":     map[string]any{"type": "string", "maxLength": inboxstore.MaxTitle},
		"notes":     map[string]any{"type": "string", "maxLength": inboxstore.MaxNotes},
		"done_when": map[string]any{"type": "string", "maxLength": inboxstore.MaxDoneWhen},
		"draft": map[string]any{"type": "string", "maxLength": inboxstore.MaxDraft, "description": "The reply " +
			"you drafted for the owner to send himself."},
	}

	add := map[string]any{
		"assignee_kind": map[string]any{"type": "string", "enum": []string{inboxstore.Me, inboxstore.Person,
			inboxstore.ABot, inboxstore.Unknown}, "description": "Who does it: me, the owner (the default); a " +
			"person he waits on; a Bot, by its id in assignee."},
		"assignee": map[string]any{"type": "string", "description": "The person's name, or the Bot's id."},
		"state": map[string]any{"type": "string", "enum": []string{inboxstore.Open, inboxstore.Proposed},
			"description": "Left out: open when it comes from this chat; proposed when it comes from this " +
				"Routine's run or from what you read (source), or open there with why_open."},
		"why_open": map[string]any{"type": "string", "enum": []string{inboxstore.AskedDirectly, inboxstore.Promised},
			"description": "For an item from what you read or from a Routine's run: why it opens, with quote and " +
				"the message."},
		"key": map[string]any{"type": "string", "maxLength": inboxstore.MaxKey,
			"description": "The same key again returns the item it made."},
	}
	// Where it was read: the same in inbox_add and in inbox_update, which points an item at it.
	read := map[string]any{
		"source": map[string]any{
			"type": "object", "required": []string{"kind"},
			"description": "What of the owner's you read it in, in this chat or a Routine's run: his Zalo, " +
				"WhatsApp or Lark chat, his Gmail thread, or a meeting he recorded. Left out, this chat or this run.",
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "enum": inboxstore.FoundKinds},
				"chat": map[string]any{"type": "string", "description": "zalo, whatsapp, lark: the chat's id at " +
					"the service."},
				"thread":  map[string]any{"type": "string", "description": "gmail: the thread's id."},
				"meeting": map[string]any{"type": "string", "description": "meeting: the meeting's id."},
				"account": map[string]any{"type": "string", "description": "The owner's account you read it " +
					"with, as your tools name it (the label of zalo:<label>, google:<label>…); every kind but " +
					"meeting. One you may not use is refused."},
			},
		},
		"quote": map[string]any{"type": "string", "maxLength": inboxstore.MaxQuote,
			"description": "The words it came from."},
		"message": inboxLinkSchema("The message it came from: lark_message, zalo_message or whatsapp_message " +
			"for a chat there; mail, the Gmail message's id; meeting_line, \"<meeting id>/<line>\"; chat_message " +
			"for this chat's."),
	}

	tools := []struct {
		name, about string
		props       map[string]any
		required    []string
	}{
		{agenttool.ToolInboxList, "List the owner's to-dos, by due then age: open ones by default. Filter by " +
			"state, who does them, where they came from, when they are due, needs_you (what waits for the owner), and include closed, " +
			"snoozed or stale proposals; cursor pages on. Use it before you add one, so as never to add it twice.", map[string]any{
			"state": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{
				inboxstore.Proposed, inboxstore.Open, inboxstore.Doing, inboxstore.Review, inboxstore.Done,
				inboxstore.Dropped, inboxstore.Duplicate}}},
			"assignee_kind":   add["assignee_kind"],
			"assignee":        add["assignee"],
			"source_kind":     map[string]any{"type": "string"},
			"needs_you":       map[string]any{"type": "boolean"},
			"include_closed":  map[string]any{"type": "boolean"},
			"include_snoozed": map[string]any{"type": "boolean"},
			"include_stale":   map[string]any{"type": "boolean"},
			"due_from": map[string]any{"type": "string", "description": "Only the items due at or after this " +
				"moment, RFC 3339; a date is due from its first moment."},
			"due_to": map[string]any{"type": "string", "description": "Only the items due before this moment, " +
				"RFC 3339."},
			"owner_changed_since": map[string]any{"type": "string", "description": "Only the items the owner " +
				"himself added, edited, closed, reopened or snoozed at or after this moment, RFC 3339, in any " +
				"state, each with his changes since then as its history: what he corrected of yours."},
			"cursor": map[string]any{"type": "string"},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": inboxstore.MaxPage},
		}, nil},
		{agenttool.ToolInboxGet, "Read one to-do with its draft, evidence and latest history. Use it before you " +
			"change one you have not just read.",
			map[string]any{"id": id}, []string{"id"}},
		{agenttool.ToolInboxAdd, "Add a to-do, from this chat, this Routine's run, or what of the owner's you " +
			"read (source). Use it for each direct ask to the owner and each promise he makes: give the quote " +
			"and the message it came from. An item given to a person without a due is due in 2 days, the day " +
			"to chase them.", merged(fields, due, add, read), []string{"title"}},
		{agenttool.ToolInboxUpdate, "Change a to-do you added or were given: its fields, the reply you drafted " +
			"(draft), where you read it (source, with quote and message), hand_back to give it to the " +
			"owner, where it waits in Needs you, or pass_to to give it to another Bot. An item you are doing is " +
			"released first. Use it to attach a reply you drafted and give it to him to send.",
			merged(fields, due, read, map[string]any{
				"id": id, "revision": revision,
				"hand_back": map[string]any{"type": "boolean", "description": "Give it back to the owner."},
				"pass_to": map[string]any{"type": "string", "description": "Give it to this Bot, by its id; it is " +
					"woken with it. The main Bot passes it to the project Bot whose accounts or context the work " +
					"needs; a project Bot gives it back only to the main Bot."},
			}), []string{"id"}},
		{agenttool.ToolInboxStart, "Start an open to-do given to you: it is doing, here, until you close or " +
			"release it. Use it before you work on one.", map[string]any{"id": id, "revision": revision}, []string{"id"}},
		{agenttool.ToolInboxRelease, "Give up a to-do you are doing, with the reason: it is open again. Use it " +
			"when you cannot finish it.",
			map[string]any{"id": id, "revision": revision, "reason": reason}, []string{"id", "reason"}},
		{agenttool.ToolInboxDone, "Close a to-do as done, with evidence: what you did, or, with for_owner, his " +
			"own message that he did it (in this chat, or one he sent in the item's Zalo, WhatsApp or Lark chat " +
			"after it was made: zalo_sent, whatsapp_sent, lark_sent). Use it once the work is done or he answered.", map[string]any{
			"id": id, "revision": revision, "evidence": evidence, "reason": reason,
			"for_owner": map[string]any{"type": "boolean", "description": "The owner did it; you record it."},
		}, []string{"id", "evidence"}},
		{agenttool.ToolInboxComment, "Post a comment in a to-do's thread, which the owner reads on the item: " +
			"what you started, the Run's link, the pull request, what you need from him, done. Use it on an item " +
			"you added or were given, to tell him where its work is; his comments on it reach you in its chat.",
			map[string]any{"id": id, "text": map[string]any{"type": "string", "maxLength": inboxstore.MaxComment}},
			[]string{"id", "text"}},
		{agenttool.ToolInboxDrop, "Drop a to-do the owner said is not needed, with his message as evidence and " +
			"the reason. Use it only on his word.", map[string]any{"id": id, "revision": revision, "reason": reason, "evidence": evidence},
			[]string{"id", "reason", "evidence"}},
	}

	out := make([]agenttool.BrowserTool, 0, len(tools))

	for _, t := range tools {
		schema := map[string]any{"type": "object", "properties": t.props}
		if len(t.required) > 0 {
			schema["required"] = t.required
		}

		raw, _ := json.Marshal(schema) //nolint:errchkjson // maps of strings and slices always encode

		out = append(out, agenttool.BrowserTool{Name: t.name, Description: t.about, InputSchema: raw})
	}

	return out
}

func merged(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, p := range parts {
		for k, v := range p {
			out[k] = v
		}
	}

	return out
}

// inboxArgs are every Inbox tool's arguments.
type inboxArgs struct {
	ID       string  `json:"id"`
	Revision int     `json:"revision"`
	Title    *string `json:"title"`
	Notes    *string `json:"notes"`
	DoneWhen *string `json:"done_when"`
	Draft    *string `json:"draft"`
	DueDate  string  `json:"due_date"`
	DueAt    string  `json:"due_at"`
	Timezone string  `json:"timezone"`
	HandBack bool    `json:"hand_back"`
	PassTo   string  `json:"pass_to"`
	// inbox_add.
	AssigneeKind string `json:"assignee_kind"`
	Assignee     string `json:"assignee"`
	State        string `json:"state"`
	WhyOpen      string `json:"why_open"`
	Key          string `json:"key"`
	// inbox_add and inbox_update: where it was read.
	Source  *inboxSourceArgs `json:"source"`
	Quote   string           `json:"quote"`
	Message *inboxstore.Link `json:"message"`
	// inbox_comment.
	Text string `json:"text"`
	// inbox_release, inbox_done, inbox_drop.
	Reason   string            `json:"reason"`
	Evidence []inboxstore.Link `json:"evidence"`
	ForOwner bool              `json:"for_owner"`
}

// inboxListArgs are inbox_list's arguments.
type inboxListArgs struct {
	States         []string `json:"state"`
	AssigneeKind   string   `json:"assignee_kind"`
	Assignee       string   `json:"assignee"`
	SourceKind     string   `json:"source_kind"`
	NeedsYou       bool     `json:"needs_you"`
	IncludeClosed  bool     `json:"include_closed"`
	IncludeSnoozed bool     `json:"include_snoozed"`
	IncludeStale   bool     `json:"include_stale"`
	DueFrom        string   `json:"due_from"`
	DueTo          string   `json:"due_to"`
	// OwnerChangedSince lists the items the owner changed since a moment, each with his changes (issue #1119).
	OwnerChangedSince string `json:"owner_changed_since"`
	Cursor            string `json:"cursor"`
	Limit             int    `json:"limit"`
}

type inboxSourceArgs struct {
	Kind    string `json:"kind"`
	Chat    string `json:"chat"`
	Thread  string `json:"thread"`
	Meeting string `json:"meeting"`
	Account string `json:"account"`
}

// sourceIntegration is the Integration whose Connection a source names its account by.
var sourceIntegration = map[string]string{
	inboxstore.FromZalo: "zalo", inboxstore.FromWhatsApp: "whatsapp", inboxstore.FromLark: "lark",
	inboxstore.FromGmail: "google",
}

// readSource is where a Bot read an item, as it names it (issue #1105): what of the owner's it read, in any of its
// chats or runs, accepted only when it may read it now as its reads are: an account it may use (Bot.MayUse, as
// agentapi's tools and the service proxy check it), or a meeting that is there for a Bot that reads meetings. The
// provenance is the token's still: the Bot names only what it may read, as the client API's newSource takes the
// chat or run from the token (issue #966).
func (s *Server) readSource(ctx context.Context, a agent, in inboxArgs) (inboxstore.Source, error) {
	src := inboxstore.Source{Kind: in.Source.Kind, Account: strings.TrimSpace(in.Source.Account), Quote: in.Quote,
		Ref: in.Message}

	switch src.Kind {
	case inboxstore.FromZalo, inboxstore.FromWhatsApp, inboxstore.FromLark:
		src.ID = in.Source.Chat
	case inboxstore.FromGmail:
		src.ID = in.Source.Thread
	case inboxstore.FromMeeting:
		src.ID = in.Source.Meeting
	default:
		return inboxstore.Source{}, fmt.Errorf("%w: a to-do you read comes from %s, not %q; left out, it comes "+
			"from this run or this chat", orchestration.ErrForbidden, strings.Join(inboxstore.FoundKinds, ", "),
			src.Kind)
	}

	src.ID = strings.TrimSpace(src.ID)
	if src.ID == "" {
		return inboxstore.Source{}, fmt.Errorf("%w: a to-do from %s names its chat, thread or meeting",
			orchestration.ErrInvalid, src.Kind)
	}

	if src.Kind == inboxstore.FromMeeting {
		if !s.readsMeetings(a) {
			return inboxstore.Source{}, fmt.Errorf("%w: you do not read the owner's meetings", orchestration.ErrForbidden)
		}

		if _, err := s.meetings.Get(ctx, src.ID); err != nil {
			return inboxstore.Source{}, err
		}

		return src, nil
	}

	integration := sourceIntegration[src.Kind]
	if src.Account == "" {
		return inboxstore.Source{}, fmt.Errorf("%w: a to-do from %s names the owner's account you read it with, "+
			"the label of %s:<label>", orchestration.ErrInvalid, src.Kind, integration)
	}

	may, err := s.offeredTo(ctx, a, integration)
	if err != nil {
		return inboxstore.Source{}, err
	}

	if !slices.ContainsFunc(may, func(c orchestration.Connection) bool { return c.Label == src.Account }) {
		return inboxstore.Source{}, fmt.Errorf("%w: you may not use %s:%s, so nothing is added from it",
			orchestration.ErrForbidden, integration, src.Account)
	}

	return src, nil
}

// inboxCall runs one of the Inbox's tools. What the Bot can act on (its arguments, a conflict, a refusal) is its
// result, marked as an error.
func (s *Server) inboxCall(w http.ResponseWriter, r *http.Request, a agent, call agenttool.BrowserCall) {
	var (
		in   inboxArgs
		list inboxListArgs
	)

	raw := orEmpty(call.Arguments)

	bad := json.Unmarshal(raw, &in)
	if call.Tool == agenttool.ToolInboxList {
		bad = json.Unmarshal(raw, &list)
	}

	if bad != nil {
		answerTool(w, call.Tool+" takes a JSON object matching its tool schema", true)

		return
	}

	ctx := r.Context()

	c, err := s.inboxCallerOf(ctx, a)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	var out any

	switch call.Tool {
	case agenttool.ToolInboxList:
		out, err = s.inboxList(ctx, list)
	case agenttool.ToolInboxGet:
		var t inboxstore.Todo
		if t, err = s.store.Inbox().Get(ctx, strings.TrimSpace(in.ID)); err == nil {
			out = todoView(t, true)
		}
	case agenttool.ToolInboxAdd:
		out, err = s.inboxAdd(ctx, a, c, in)
	case agenttool.ToolInboxComment:
		var t inboxstore.Todo
		if t, err = s.store.Inbox().Comment(ctx, c.actor, strings.TrimSpace(in.ID), in.Text); err == nil {
			out = todoView(t, true)
		}
	default:
		out, err = s.inboxChange(ctx, a, c, call.Tool, in)
	}

	switch {
	case err == nil:
		s.logger.InfoContext(ctx, "a bot used the inbox", slog.String("event", "agentapi.inbox"),
			slog.String("agent", a.name), slog.String("tool", call.Tool))
		s.answerJSON(w, r, out, nil)
	case errors.Is(err, orchestration.ErrInvalid) || errors.Is(err, orchestration.ErrNotFound) ||
		errors.Is(err, orchestration.ErrConflict) || errors.Is(err, orchestration.ErrForbidden) ||
		errors.Is(err, orchestration.ErrUnavailable):
		answerTool(w, err.Error(), true)
	default:
		s.fail(w, r, err)
	}
}

func (s *Server) inboxList(ctx context.Context, in inboxListArgs) (any, error) {
	f := inboxstore.Filter{
		States: in.States, SourceKind: in.SourceKind, NeedsYou: in.NeedsYou, WithClosed: in.IncludeClosed,
		WithSnoozed: in.IncludeSnoozed, WithStale: in.IncludeStale,
	}

	if in.AssigneeKind != "" {
		f.Assignee = &inboxstore.Assignee{Kind: in.AssigneeKind, Name: strings.TrimSpace(in.Assignee)}
	}

	for _, bound := range []struct {
		name, value string
		into        *time.Time
	}{
		{"due_from", in.DueFrom, &f.DueFrom}, {"due_to", in.DueTo, &f.DueTo},
		{"owner_changed_since", in.OwnerChangedSince, &f.OwnerChangedSince},
	} {
		if bound.value == "" {
			continue
		}

		at, err := time.Parse(time.RFC3339, bound.value)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is RFC 3339, such as 2026-11-21T00:00:00+07:00", orchestration.ErrInvalid,
				bound.name)
		}

		*bound.into = at
	}

	page, err := s.store.Inbox().List(ctx, f, in.Cursor, in.Limit)
	if err != nil {
		return nil, err
	}

	items := make([]todoOut, 0, len(page.Items))
	for _, t := range page.Items {
		items = append(items, todoView(t, !f.OwnerChangedSince.IsZero()))
	}

	return map[string]any{"total": page.Total, "next_cursor": page.Next, "items": items}, nil
}

// inboxDue is the due a call gives: none, a date or a moment in its time zone.
func inboxDue(in inboxArgs) (due inboxstore.Due, given bool, err error) {
	switch {
	case in.DueDate != "" && in.DueAt != "":
		return due, false, fmt.Errorf("%w: give due_date or due_at, not both", orchestration.ErrInvalid)
	case in.DueDate != "":
		return inboxstore.Due{Kind: inboxstore.DueDate, Date: in.DueDate, Zone: in.Timezone}, true, nil
	case in.DueAt != "":
		at, err := time.Parse(time.RFC3339, in.DueAt)
		if err != nil {
			return due, false, fmt.Errorf("%w: due_at is RFC 3339, such as 2026-11-21T17:00:00+07:00",
				orchestration.ErrInvalid)
		}

		return inboxstore.Due{Kind: inboxstore.DueInstant, At: at, Zone: in.Timezone}, true, nil
	}

	return due, false, nil
}

// inboxChase is the due of an item given to a person when the Bot gives none: the day to chase them, counted by the
// store from when the item is first made.
func inboxChase(in inboxArgs) *inboxstore.DueIn {
	zone := in.Timezone
	if zone == "" {
		zone = "UTC"
	}

	return &inboxstore.DueIn{After: inboxDefaultChase, Zone: zone}
}

func (s *Server) inboxAdd(ctx context.Context, a agent, c inboxCaller, in inboxArgs) (any, error) {
	n := inboxstore.NewTodo{
		Title: val(in.Title), Notes: val(in.Notes), DoneWhen: val(in.DoneWhen), Draft: val(in.Draft),
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me}, WhyOpen: in.WhyOpen, IdempotencyKey: in.Key,
		Source: inboxstore.Source{Quote: in.Quote, Ref: in.Message},
	}

	if in.AssigneeKind != "" {
		n.Assignee = inboxstore.Assignee{Kind: in.AssigneeKind, Name: in.Assignee}
	}

	due, given, err := inboxDue(in)
	if err != nil {
		return nil, err
	}

	switch {
	case given:
		n.Due = due
	case n.Assignee.Kind == inboxstore.Person:
		n.DueIn = inboxChase(in)
	}

	// Where it came from: what of the owner's the Bot read, in any of its chats or runs (issue #1105), or else the
	// token's chat or Routine's run, and nothing else: a run naming a chat with the owner would open what it only
	// infers (clientapi's newSource).
	switch {
	case in.Source != nil:
		src, err := s.readSource(ctx, a, in)
		if err != nil {
			return nil, err
		}

		n.Source = src
	case c.work.Kind == inboxstore.WorkRun:
		n.Source.Kind, n.Source.ID = inboxstore.FromRoutine, c.work.ID
	default:
		n.Source.Kind, n.Source.ID = inboxstore.FromChat, c.work.ID
	}

	// The triage of #966 wherever the Bot is: what it read opens with why_open, else it is proposed; what the owner
	// tells it in its chat opens.
	switch {
	case in.State != "":
		n.State = in.State
	case n.WhyOpen != "" || n.Source.Kind == inboxstore.FromChat:
		n.State = inboxstore.Open
	default:
		n.State = inboxstore.Proposed
	}

	t, err := s.store.Inbox().Create(ctx, c.actor, n)
	if err != nil {
		return nil, err
	}

	return todoView(t, false), nil
}

// inboxChange runs a change of an item: it reads the item, and writes at the revision the Bot gave or the one read,
// in the item's attempt when the Bot is doing it here.
func (s *Server) inboxChange(ctx context.Context, a agent, c inboxCaller, tool string, in inboxArgs) (any, error) {
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		return nil, fmt.Errorf("%w: name the to-do by its id", orchestration.ErrInvalid)
	}

	inbox := s.store.Inbox()

	cur, err := inbox.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}

	ch := inboxstore.Change{Actor: c.actor, ID: cur.ID, Revision: cur.Revision, Chat: c.chat()}
	if in.Revision != 0 {
		ch.Revision = in.Revision
	}

	if cur.Work != nil && cur.Work.Bot == a.bot && cur.Work.Kind == c.work.Kind && cur.Work.ID == c.work.ID {
		ch.Attempt = cur.Work.Attempt
	}

	var t inboxstore.Todo

	switch tool {
	case agenttool.ToolInboxUpdate:
		e := inboxstore.Edit{Title: in.Title, Notes: in.Notes, DoneWhen: in.DoneWhen, Draft: in.Draft}

		due, given, dueErr := inboxDue(in)
		if dueErr != nil {
			return nil, dueErr
		}

		if given {
			e.Due = &due
		}

		pass := strings.TrimSpace(in.PassTo)

		switch {
		case in.HandBack && pass != "":
			return nil, fmt.Errorf("%w: hand_back or pass_to, not both", orchestration.ErrInvalid)
		case in.HandBack:
			e.Assignee = &inboxstore.Assignee{Kind: inboxstore.Me}
		case pass != "":
			e.Assignee = &inboxstore.Assignee{Kind: inboxstore.ABot, Name: pass}
		}

		if in.Source != nil {
			src, srcErr := s.readSource(ctx, a, in)
			if srcErr != nil {
				return nil, srcErr
			}

			e.Source = &src
		}

		t, err = inbox.Edit(ctx, ch, e)
	case agenttool.ToolInboxStart:
		t, err = inbox.Start(ctx, ch, c.work)
	case agenttool.ToolInboxRelease:
		t, err = inbox.Release(ctx, ch, in.Reason)
	case agenttool.ToolInboxDone:
		cl := inboxstore.Closing{
			DoneBy: inboxstore.Assignee{Kind: inboxstore.ABot, Name: a.bot}, How: inboxstore.ClosedByBot,
			Reason: in.Reason, Evidence: in.Evidence,
		}

		if in.ForOwner {
			cl.DoneBy = inboxstore.Assignee{Kind: inboxstore.Me}
		}

		t, err = inbox.Done(ctx, ch, cl)
	case agenttool.ToolInboxDrop:
		t, err = inbox.Drop(ctx, ch, in.Reason, in.Evidence)
	default:
		return nil, fmt.Errorf("%w: no tool %s", orchestration.ErrNotFound, tool)
	}

	if err != nil {
		return nil, err
	}

	return todoView(t, false), nil
}

// todoOut is a to-do as a Bot reads it: what it needs to act, without the attempt it never handles.
type todoOut struct {
	ID          string            `json:"id"`
	Revision    int               `json:"revision"`
	Title       string            `json:"title"`
	State       string            `json:"state"`
	Notes       string            `json:"notes,omitempty"`
	DoneWhen    string            `json:"done_when,omitempty"`
	Draft       string            `json:"draft,omitempty"`
	Due         string            `json:"due,omitempty"`
	Assignee    string            `json:"assignee"`
	PassedBy    string            `json:"passed_by,omitempty"`
	Source      map[string]any    `json:"source"`
	WhyOpen     string            `json:"why_open,omitempty"`
	CreatedBy   string            `json:"created_by"`
	Doing       string            `json:"doing,omitempty"`
	ApprovalID  string            `json:"approval_id,omitempty"`
	Snoozed     *time.Time        `json:"snoozed_until,omitempty"`
	Stale       bool              `json:"stale,omitempty"`
	DoneBy      string            `json:"done_by,omitempty"`
	CloseReason string            `json:"close_reason,omitempty"`
	Evidence    []inboxstore.Link `json:"evidence,omitempty"`
	ClosedAt    *time.Time        `json:"closed_at,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	History     []map[string]any  `json:"history,omitempty"`
}

func who(kind, name string) string {
	if name == "" {
		return kind
	}

	return kind + " " + name
}

func todoView(t inboxstore.Todo, events bool) todoOut {
	out := todoOut{
		ID: t.ID, Revision: t.Revision, Title: t.Title, State: t.State, Notes: t.Notes, DoneWhen: t.DoneWhen,
		Draft: t.Draft, Assignee: who(t.Assignee.Kind, t.Assignee.Name), WhyOpen: t.WhyOpen,
		CreatedBy: who(t.CreatedBy.Kind, t.CreatedBy.ID), ApprovalID: t.ApprovalID, Snoozed: t.SnoozedUntil,
		Stale: t.Stale, CloseReason: t.CloseReason, Evidence: t.Evidence, ClosedAt: t.ClosedAt,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Source: map[string]any{"kind": t.Source.Kind},
	}

	switch t.Due.Kind {
	case inboxstore.DueDate:
		out.Due = t.Due.Date + " (" + t.Due.Zone + ")"
	case inboxstore.DueInstant:
		if loc, err := time.LoadLocation(t.Due.Zone); err == nil {
			out.Due = t.Due.At.In(loc).Format(time.RFC3339)
		}
	case inboxstore.DueUnknown:
		out.Due = "unknown"
	}

	for k, v := range map[string]string{"id": t.Source.ID, "account": t.Source.Account, "quote": t.Source.Quote} {
		if v != "" {
			out.Source[k] = v
		}
	}

	if t.Source.Ref != nil {
		out.Source["message"] = t.Source.Ref
	}

	if t.PassedBy != "" {
		out.PassedBy = who(inboxstore.Bot, t.PassedBy)
	}

	if t.Work != nil {
		out.Doing = fmt.Sprintf("bot %s in %s %s", t.Work.Bot, t.Work.Kind, t.Work.ID)
	}

	if t.DoneBy != nil {
		out.DoneBy = who(t.DoneBy.Kind, t.DoneBy.Name)
	}

	if events {
		for _, e := range t.Events[max(0, len(t.Events)-inboxEventsShown):] {
			var after map[string]any

			_ = json.Unmarshal(e.After, &after)
			delete(after, "work_attempt")

			out.History = append(out.History, map[string]any{
				"at": e.At, "by": who(e.Actor.Kind, e.Actor.ID), "kind": e.Kind, "after": after,
			})
		}
	}

	return out
}

// isInboxTool says whether a tool is one of the Inbox's.
func isInboxTool(tool string) bool { return slices.Contains(agenttool.InboxTools, tool) }

func val[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}
