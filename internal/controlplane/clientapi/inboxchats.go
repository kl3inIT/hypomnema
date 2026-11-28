package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/inbox"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// Which of the owner's chats the secretary reads (issue #1270): his accounts' chats, each with whether the
// triage's check reads it and why, his switch per chat, and the one-time move of a Bot's dropChats ids into his
// choices. Only the owner's token reaches them (policy.go); a Bot reads the choices with inbox_chats.

// InboxChats lists a Connection's chats with the owner's choices, and moves a Bot's dropChats into them.
type InboxChats interface {
	List(ctx context.Context, integration, label string) ([]inbox.Chat, error)
	MoveDropChats(ctx context.Context, sources []inbox.Source, patterns []string,
		add func(ctx context.Context, integration, label, chat string, reads bool) (bool, error),
	) (inbox.Moved, error)
}

// WithInboxChats sets what lists the chats the secretary reads.
func (s *Server) WithInboxChats(c InboxChats) *Server {
	s.inboxChats = c

	return s
}

var errNoInboxChats = fmt.Errorf("%w: this control plane lists no chats for the secretary",
	orchestration.ErrUnavailable)

// secretaryIntegrations are the services whose chats the triage reads: his messaging accounts and his own Lark
// sign-ins (deploy/thu-ky/new-messages.mjs sources), and his Telegram (#1184).
var secretaryIntegrations = []string{"zalo", "whatsapp", "telegram", "lark"}

// chatSources are the owner's Connections whose chats the secretary may read: every Zalo, WhatsApp and Telegram
// account, and
// the Lark Connections that sign in as him (an app's bot reads none of his chats, issue #1127).
func (s *Server) chatSources(ctx context.Context) ([]orchestration.Connection, error) {
	out := []orchestration.Connection{}

	for _, integration := range secretaryIntegrations {
		conns, err := s.svc.ConnectionsOf(ctx, integration)
		if err != nil {
			return nil, err
		}

		for _, c := range conns {
			if c.User() {
				out = append(out, c)
			}
		}
	}

	return out, nil
}

// inboxChatDefaults are what a chat the owner never set takes, by its kind.
func inboxChatDefaults() openapi.InboxChatDefaults {
	return openapi.InboxChatDefaults{
		Direct: inboxstore.DefaultReads(inboxstore.ChatDirect), Group: inboxstore.DefaultReads(inboxstore.ChatGroup),
		Bot: inboxstore.DefaultReads(inboxstore.ChatBot), BotPatterns: slices.Clone(inboxstore.BotPatterns),
	}
}

func countsOut(n inbox.Counts) openapi.InboxChatCounts {
	return openapi.InboxChatCounts{
		All: n.All, Read: n.Read, Skipped: n.Skipped, Direct: n.Direct, Group: n.Group, Bot: n.Bot,
	}
}

// ListInboxChatSources lists the owner's accounts whose chats the secretary may read, each counted.
func (s *Server) ListInboxChatSources(ctx context.Context, _ openapi.ListInboxChatSourcesRequestObject,
) (openapi.ListInboxChatSourcesResponseObject, error) {
	if s.inboxChats == nil {
		return nil, errNoInboxChats
	}

	conns, err := s.chatSources(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListInboxChatSources200JSONResponse{
		Data: make([]openapi.InboxChatSource, 0, len(conns)), Defaults: inboxChatDefaults(),
	}

	for _, c := range conns {
		src := openapi.InboxChatSource{Integration: c.Integration, Label: c.Label, Account: c.Account}

		chats, err := s.inboxChats.List(ctx, c.Integration, c.Label)
		if err != nil {
			src.Problem = ptr(err.Error())
		} else {
			src.Counts = ptr(countsOut(inbox.CountOf(chats)))
		}

		out.Data = append(out.Data, src)
	}

	return out, nil
}

// chatSource is the owner's Connection of that name whose chats the secretary may read.
func (s *Server) chatSource(ctx context.Context, integration, label string) error {
	conns, err := s.chatSources(ctx)
	if err != nil {
		return err
	}

	if !slices.ContainsFunc(conns, func(c orchestration.Connection) bool {
		return c.Integration == integration && c.Label == label
	}) {
		return fmt.Errorf("%w: %s:%s is not an account of the owner's whose chats the secretary reads",
			orchestration.ErrNotFound, integration, label)
	}

	return nil
}

// defaultInboxChats is how many chats a list gives when it names no limit.
const defaultInboxChats = 500

// ListInboxChats lists an account's chats with whether the secretary reads each, searched and filtered.
func (s *Server) ListInboxChats(ctx context.Context, req openapi.ListInboxChatsRequestObject,
) (openapi.ListInboxChatsResponseObject, error) {
	if s.inboxChats == nil {
		return nil, errNoInboxChats
	}

	if err := s.chatSource(ctx, req.Integration, req.Label); err != nil {
		return nil, err
	}

	chats, err := s.inboxChats.List(ctx, req.Integration, req.Label)
	if err != nil {
		return nil, err
	}

	searched := chats
	var searchCounts *openapi.InboxChatCounts

	if q := strings.TrimSpace(val(req.Params.Q)); q != "" {
		searched = inbox.Filter(chats, q, "", "")
		searchCounts = ptr(countsOut(inbox.CountOf(searched)))
	}

	kept := inbox.Filter(searched, "", string(val(req.Params.Kind)), string(val(req.Params.State)))
	limit := defaultInboxChats
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}

	out := openapi.ListInboxChats200JSONResponse{
		Data: make([]openapi.InboxChat, 0, min(len(kept), limit)), Counts: countsOut(inbox.CountOf(chats)),
		Matched: len(kept), SearchCounts: searchCounts,
	}

	for _, ch := range kept[:min(len(kept), limit)] {
		out.Data = append(out.Data, inboxChatOut(ch))
	}

	return out, nil
}

func inboxChatOut(ch inbox.Chat) openapi.InboxChat {
	out := openapi.InboxChat{
		Id: ch.ID, Name: ch.Name, Kind: openapi.InboxChatKind(ch.Kind), Reads: ch.Reads, SetBy: openapi.InboxChatByDefault,
	}
	if !ch.LastMessageAt.IsZero() {
		out.LastMessageAt = ptr(ch.LastMessageAt)
	}

	if ch.ByOwner {
		out.SetBy, out.SetAt = openapi.InboxChatByOwner, ptr(ch.SetAt)
	}

	return out
}

// SetInboxChat records the owner's choice for one chat of his account.
func (s *Server) SetInboxChat(ctx context.Context, req openapi.SetInboxChatRequestObject,
) (openapi.SetInboxChatResponseObject, error) {
	if err := s.chatSource(ctx, req.Integration, req.Label); err != nil {
		return nil, err
	}

	r, err := s.svc.Inbox().SetChatRead(ctx, req.Integration, req.Label, req.Chat, req.Body.Reads)
	if err != nil {
		return nil, err
	}

	return openapi.SetInboxChat200JSONResponse{
		Integration: r.Integration, Label: r.Label, Chat: r.Chat, Reads: r.Reads, SetAt: r.At.In(time.UTC),
	}, nil
}

// dropChatsFile is the triage's filters in a Bot's folder (deploy/thu-ky/new-messages.mjs, configFile).
const dropChatsFile = "thu-ky/filters.json"

// MoveInboxDropChats moves the chat ids of a Bot's dropChats into the owner's choices, once.
func (s *Server) MoveInboxDropChats(ctx context.Context, req openapi.MoveInboxDropChatsRequestObject,
) (openapi.MoveInboxDropChatsResponseObject, error) {
	if s.inboxChats == nil {
		return nil, errNoInboxChats
	}

	if s.botFiles == nil {
		return nil, errNoWorkspaces
	}

	ref := val(req.Body.Bot)
	if ref == "" {
		ref = orchestration.DefaultBotID
	}

	bot, err := s.svc.GetBot(ctx, ref)
	if err != nil {
		return nil, err
	}

	patterns, err := s.dropChatsOf(ctx, bot.ID)
	if err != nil {
		return nil, err
	}

	conns, err := s.chatSources(ctx)
	if err != nil {
		return nil, err
	}

	sources := make([]inbox.Source, 0, len(conns))
	for _, c := range conns {
		sources = append(sources, inbox.Source{Integration: c.Integration, Label: c.Label})
	}

	moved, err := s.inboxChats.MoveDropChats(ctx, sources, patterns, s.svc.Inbox().AddChatRead)
	if err != nil {
		return nil, err
	}

	refs := func(chats []inbox.MovedChat) []openapi.InboxChatRef {
		out := make([]openapi.InboxChatRef, 0, len(chats))
		for _, c := range chats {
			out = append(out, openapi.InboxChatRef{Integration: c.Integration, Label: c.Label, Chat: c.Chat})
		}

		return out
	}
	list := func(s []string) []string { return append([]string{}, s...) }

	return openapi.MoveInboxDropChats200JSONResponse{
		Moved: refs(moved.Moved), Kept: refs(moved.Kept), Unknown: list(moved.Unknown), Left: list(moved.Left),
		Defaults: list(moved.Defaults), NotRead: list(moved.NotRead),
	}, nil
}

// dropChatsOf reads dropChats of the Bot's thu-ky/filters.json; a Bot without the file, or without the field, has
// none.
func (s *Server) dropChatsOf(ctx context.Context, bot string) ([]string, error) {
	res, err := s.botFiles.Ask(ctx, bot, workspacefs.Request{Op: workspacefs.OpRead, Path: dropChatsFile})
	if err != nil {
		return nil, err
	}

	if res.Error != nil {
		if res.Error.Code == workspacefs.CodeNotFound {
			return nil, nil
		}

		return nil, fmt.Errorf("reading %s of bot %s: %w", dropChatsFile, bot, res.Error)
	}

	if res.File == nil || res.File.Binary || res.File.Truncated {
		return nil, fmt.Errorf("%w: %s of bot %s is not a JSON file", orchestration.ErrInvalid, dropChatsFile, bot)
	}

	var filters struct {
		DropChats []string `json:"dropChats"`
	}

	if err := json.Unmarshal([]byte(res.File.Content), &filters); err != nil {
		return nil, fmt.Errorf("%w: %s of bot %s: %w", orchestration.ErrInvalid, dropChatsFile, bot, err)
	}

	return filters.DropChats, nil
}
