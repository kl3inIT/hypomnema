package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Which of the owner's chats the secretary reads (issue #1270), from the terminal: his accounts with their counts,
// an account's chats with a search and filters, his switch per chat, and the one-time move of a Bot's dropChats ids
// into his choices, which deploy/thu-ky/setup.sh runs.

// accountOf reads <integration>:<label>.
func accountOf(s string) (string, string, error) {
	integration, label, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || integration == "" || label == "" {
		return "", "", fmt.Errorf("%q is not <integration>:<label>, such as zalo:main", s)
	}

	return integration, label, nil
}

func inboxChats(ctx context.Context, e *env, fs *flag.FlagSet) error {
	if fs.NArg() == 0 {
		resp, err := e.api.ListInboxChatSourcesWithResponse(ctx)
		if err != nil {
			return fmt.Errorf("listing the chat sources: %w", err)
		}

		if resp.JSON200 == nil {
			return apiError("listing the chat sources", resp.StatusCode(), resp.JSONDefault)
		}

		return e.print(fs, resp.JSON200, func() {
			for _, s := range resp.JSON200.Data {
				e.printf("%s:%s\t%s\t", s.Integration, s.Label, s.Account)

				if s.Counts != nil {
					e.printf("%d chats, %d read, %d skipped\n", s.Counts.All, s.Counts.Read, s.Counts.Skipped)
				} else {
					e.printf("cannot be listed: %s\n", val(s.Problem))
				}
			}
		})
	}

	integration, label, err := accountOf(fs.Arg(0))
	if err != nil {
		return err
	}

	params := &openapi.ListInboxChatsParams{}
	if q := str(fs, "q"); q != "" {
		params.Q = &q
	}

	if k := str(fs, "kind"); k != "" {
		kind := openapi.InboxChatKind(k)
		params.Kind = &kind
	}

	if st := str(fs, "state"); st != "" {
		state := openapi.InboxChatState(st)
		params.State = &state
	}

	resp, err := e.api.ListInboxChatsWithResponse(ctx, integration, label, params)
	if err != nil {
		return fmt.Errorf("listing the chats of %s:%s: %w", integration, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the chats of "+integration+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, c := range resp.JSON200.Data {
			state := "skipped"
			if c.Reads {
				state = "read"
			}

			e.printf("%s\t%s\t%s\t%s (%s)\n", c.Id, c.Name, c.Kind, state, c.SetBy)
		}

		e.printf("%d of %d chats\n", resp.JSON200.Matched, resp.JSON200.Counts.All)
	})
}

func inboxChatsSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	integration, label, err := accountOf(fs.Arg(0))
	if err != nil {
		return err
	}

	var reads bool

	switch fs.Arg(2) {
	case "on":
		reads = true
	case "off":
	default:
		return fmt.Errorf("%q is on or off", fs.Arg(2))
	}

	resp, err := e.api.SetInboxChatWithResponse(ctx, integration, label, fs.Arg(1), openapi.InboxChatChoice{Reads: reads})
	if err != nil {
		return fmt.Errorf("setting chat %s: %w", fs.Arg(1), err)
	}

	if resp.JSON200 == nil {
		return apiError("setting chat "+fs.Arg(1), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		e.printf("The secretary %s %s:%s chat %s.\n", map[bool]string{true: "reads", false: "skips"}[reads],
			integration, label, fs.Arg(1))
	})
}

func inboxChatsMove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	body := openapi.InboxChatMove{}
	bot := "the main Bot"

	if ref := str(fs, "bot"); ref != "" {
		id, err := botIDOf(ctx, e, ref)
		if err != nil {
			return err
		}

		body.Bot, bot = &id, ref
	}

	resp, err := e.api.MoveInboxDropChatsWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("moving the dropChats of %s: %w", bot, err)
	}

	if resp.JSON200 == nil {
		return apiError("moving the dropChats of "+bot, resp.StatusCode(), resp.JSONDefault)
	}

	m := resp.JSON200

	err = e.print(fs, m, func() {
		for _, c := range m.Moved {
			e.printf("switched off: %s:%s chat %s\n", c.Integration, c.Label, c.Chat)
		}

		for _, c := range m.Kept {
			e.printf("set already, left as it is: %s:%s chat %s\n", c.Integration, c.Label, c.Chat)
		}

		for _, id := range m.Unknown {
			e.printf("no account has chat %s; not moved\n", id)
		}

		for _, p := range m.Left {
			e.printf("not one chat's id, not moved: %s\n", p)
		}

		for _, p := range m.NotRead {
			e.printf("could not list %s\n", p)
		}

		if len(m.Defaults) > 0 {
			e.printf("%d default patterns: bots and official accounts are skipped by default\n", len(m.Defaults))
		}
	})
	if err != nil {
		return err
	}

	// An id not stored would be read by the check that replaces dropChats (review of #1270, round 1): setup.sh
	// stops on this failure before it sets that check.
	if len(m.Unknown) > 0 {
		return fmt.Errorf("%w: no account of the owner's has chat %s, so it is not stored; switch it off from the "+
			"Inbox's chats once it is listed, or take it out of dropChats of %s's thu-ky/filters.json, then run "+
			"this again", errNotMoved, strings.Join(m.Unknown, ", "), bot)
	}

	return nil
}

// errNotMoved is a move of dropChats that left an id unstored.
var errNotMoved = errors.New("not every chat id of dropChats was moved")
