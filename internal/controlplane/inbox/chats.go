package inbox

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp/syntax"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// Which of the owner's chats the secretary reads (issue #1270): the chats of each of his Connections, with whether
// the triage's check reads each and why (the default of its kind, or the owner), for his screen; and the one-time
// move of the chat ids a Bot's thu-ky/filters.json dropChats named into his choices. The choices themselves are
// inboxstore's (ChatRead). A Zalo, WhatsApp or Telegram account's chats are those the server keeps; a Lark sign-in's are
// listed by Lark (GET /open-apis/im/v1/chats, his direct chats and groups, as the triage's check lists them).

// Chats lists a Connection's chats with the owner's choices.
type Chats struct {
	kept      func(ctx context.Context, integration, label string, limit int) ([]messaging.Chat, error)
	reads     func(ctx context.Context, integration, label string) ([]inboxstore.ChatRead, error)
	larkToken func(ctx context.Context, label string) (string, error)
	larkURL   string
	client    *http.Client
}

// NewChats lists chats with c's Chats, ChatReads, LarkToken, LarkURL and Client.
func NewChats(c Config) Chats {
	return Chats{kept: c.Chats, reads: c.ChatReads, larkToken: c.LarkToken, larkURL: c.LarkURL, client: c.Client}
}

// Chat is one chat of a Connection as the secretary sees it.
type Chat struct {
	ID   string
	Name string
	// Kind is inboxstore's: direct, group or bot.
	Kind string
	// LastMessageAt is when its latest kept message was sent; zero when the service gives none (Lark).
	LastMessageAt time.Time
	// Reads says whether the triage's check reads it; ByOwner whether that is the owner's choice, made at SetAt,
	// rather than its kind's default.
	Reads   bool
	ByOwner bool
	SetAt   time.Time
}

// larkChatPages bounds the pages of a Lark sign-in's chats read for the list, 100 each.
const larkChatPages = 100

// List lists every chat of the Connection, the latest first as its service gives them, each with whether the
// secretary reads it. A chat the owner set that the list no longer has (a Lark chat he left) comes last, named by
// its id.
func (c Chats) List(ctx context.Context, integration, label string) ([]Chat, error) {
	var (
		out []Chat
		err error
	)

	switch integration {
	case "zalo", "whatsapp", "telegram":
		out, err = c.keptChats(ctx, integration, label)
	case "lark":
		out, err = c.larkChats(ctx, label)
	default:
		return nil, fmt.Errorf("%w: %s has no chats the secretary reads", storeerr.ErrInvalid, integration)
	}

	if err != nil {
		return nil, err
	}

	set, err := c.reads(ctx, integration, label)
	if err != nil {
		return nil, err
	}

	at := make(map[string]int, len(out))
	for i, ch := range out {
		at[ch.ID] = i
	}

	for _, r := range set {
		i, ok := at[r.Chat]
		if !ok {
			out = append(out, Chat{ID: r.Chat, Name: r.Chat, Kind: inboxstore.ChatKind("", "", r.Chat)})
			i = len(out) - 1
		}

		out[i].Reads, out[i].ByOwner, out[i].SetAt = r.Reads, true, r.At
	}

	return out, nil
}

func (c Chats) keptChats(ctx context.Context, integration, label string) ([]Chat, error) {
	kept, err := c.kept(ctx, integration, label, messaging.MaxChats)
	if err != nil {
		return nil, err
	}

	out := make([]Chat, 0, len(kept))
	for _, k := range kept {
		kind := inboxstore.ChatKind(k.Kind, k.Name, k.ID)
		out = append(out, Chat{
			ID: k.ID, Name: k.Name, Kind: kind, LastMessageAt: k.LastMessageAt, Reads: inboxstore.DefaultReads(kind),
		})
	}

	return out, nil
}

func (c Chats) larkChats(ctx context.Context, label string) ([]Chat, error) {
	token, err := c.larkToken(ctx, label)
	if err != nil {
		return nil, err
	}

	lark := Sent{larkURL: c.larkURL, client: c.client}
	out := []Chat{}
	page := ""

	for range larkChatPages {
		q := url.Values{"types": {"p2p,group"}, "sort_type": {"ByActiveTimeDesc"}, "page_size": {"100"}}
		if page != "" {
			q.Set("page_token", page)
		}

		var got struct {
			Data struct {
				Items []struct {
					ChatID   string `json:"chat_id"`
					Name     string `json:"name"`
					ChatMode string `json:"chat_mode"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}

		if err := lark.larkGet(ctx, token, "/open-apis/im/v1/chats?"+q.Encode(), &got); err != nil {
			return nil, err
		}

		for _, ch := range got.Data.Items {
			mode := ch.ChatMode
			if mode == "p2p" {
				mode = "dm"
			}

			kind := inboxstore.ChatKind(mode, ch.Name, ch.ChatID)
			out = append(out, Chat{
				ID: ch.ChatID, Name: cmpOr(ch.Name, ch.ChatID), Kind: kind, Reads: inboxstore.DefaultReads(kind),
			})
		}

		if !got.Data.HasMore || got.Data.PageToken == "" {
			return out, nil
		}

		page = got.Data.PageToken
	}

	return out, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// The states a list is filtered by.
const (
	StateRead    = "read"
	StateSkipped = "skipped"
)

// Filter keeps the chats whose name or id has q, in any case (wacli's `chats list --query`), of the kind and in the
// state given; an empty one keeps every chat.
func Filter(chats []Chat, q, kind, state string) []Chat {
	q = strings.ToLower(strings.TrimSpace(q))
	out := []Chat{}

	for _, ch := range chats {
		switch {
		case q != "" && !strings.Contains(strings.ToLower(ch.Name), q) && !strings.Contains(strings.ToLower(ch.ID), q):
		case kind != "" && ch.Kind != kind:
		case state == StateRead && !ch.Reads, state == StateSkipped && ch.Reads:
		default:
			out = append(out, ch)
		}
	}

	return out
}

// Counts are how many chats a Connection has, read and skipped, and of each kind.
type Counts struct {
	All, Read, Skipped, Direct, Group, Bot int
}

// CountOf counts the chats.
func CountOf(chats []Chat) Counts {
	var n Counts

	for _, ch := range chats {
		n.All++

		if ch.Reads {
			n.Read++
		} else {
			n.Skipped++
		}

		switch ch.Kind {
		case inboxstore.ChatDirect:
			n.Direct++
		case inboxstore.ChatGroup:
			n.Group++
		case inboxstore.ChatBot:
			n.Bot++
		}
	}

	return n
}

// Source is one of the owner's Connections whose chats the secretary may read.
type Source struct {
	Integration, Label string
}

// Moved is what the move of a Bot's dropChats did: each chat id it named that a Connection has, switched off by
// the owner (Moved) or left as the owner had set it already (Kept); the ids no Connection has (Unknown); and the
// patterns that are not one chat's id (Left), the defaults among them (Defaults), which the bot default holds.
type Moved struct {
	Moved, Kept    []MovedChat
	Unknown        []string
	Left, Defaults []string
	// NotRead names the sources whose chats could not be listed, with why.
	NotRead []string
}

// MovedChat is a chat of a Connection the move named.
type MovedChat struct {
	Integration, Label, Chat string
}

// MoveDropChats moves the ids of patterns, a Bot's thu-ky/filters.json dropChats, into the owner's choices (issue
// #1270): an exact id, `^<id>$`, of a chat one of the sources has is switched off as his choice, unless he made one
// for that chat already, so that running it again changes nothing. What the sources that can be listed have is
// moved; a source that cannot be listed may hold an id still to move, so the move then fails, naming the sources
// and the ids not found, and is run again once they can be (review of #1270, round 1: the check that replaces
// dropChats is set only after every id is stored).
func (c Chats) MoveDropChats(ctx context.Context, sources []Source, patterns []string,
	add func(ctx context.Context, integration, label, chat string, reads bool) (bool, error),
) (Moved, error) {
	out := Moved{}
	ids := []string{}

	for _, p := range patterns {
		switch id, ok := exactID(p); {
		case slices.Contains(inboxstore.BotPatterns, p):
			out.Defaults = append(out.Defaults, p)
		case ok:
			ids = append(ids, id)
		default:
			out.Left = append(out.Left, p)
		}
	}

	if len(ids) == 0 {
		return out, nil
	}

	found := map[string]bool{}

	for _, src := range sources {
		chats, err := c.List(ctx, src.Integration, src.Label)
		if err != nil {
			out.NotRead = append(out.NotRead, fmt.Sprintf("%s:%s: %v", src.Integration, src.Label, err))

			continue
		}

		for _, id := range ids {
			if !slices.ContainsFunc(chats, func(ch Chat) bool { return ch.ID == id }) {
				continue
			}

			found[id] = true

			added, err := add(ctx, src.Integration, src.Label, id, false)
			if err != nil {
				return Moved{}, err
			}

			chat := MovedChat{Integration: src.Integration, Label: src.Label, Chat: id}
			if added {
				out.Moved = append(out.Moved, chat)
			} else {
				out.Kept = append(out.Kept, chat)
			}
		}
	}

	for _, id := range ids {
		if !found[id] {
			out.Unknown = append(out.Unknown, id)
		}
	}

	if len(out.NotRead) > 0 {
		return out, fmt.Errorf("%w: %s; the dropChats ids not stored yet: %s. Run it again once the accounts can be "+
			"read", storeerr.ErrUnavailable, strings.Join(out.NotRead, "; "), strings.Join(out.Unknown, ", "))
	}

	return out, nil
}

// exactID is the chat id a pattern matches exactly, `^<id>$` with its metacharacters escaped, as the triage's
// README told the owner to write one.
func exactID(pattern string) (string, bool) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", false
	}

	re = re.Simplify()
	if re.Op != syntax.OpConcat || len(re.Sub) != 3 {
		return "", false
	}

	first, lit, last := re.Sub[0], re.Sub[1], re.Sub[2]
	if (first.Op != syntax.OpBeginText && first.Op != syntax.OpBeginLine) ||
		(last.Op != syntax.OpEndText && last.Op != syntax.OpEndLine) || lit.Op != syntax.OpLiteral ||
		lit.Flags&syntax.FoldCase != 0 {
		return "", false
	}

	return string(lit.Rune), true
}
