package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// chatEnv names the chat pi works for; pi's environment carries it, so the
// Runs pi creates wake that chat (issue #41, PR 3). It is a hint: pi's own
// token is bound to its chat, and the control plane refuses another one
// (issue #260).
const chatEnv = "HYPOMNEMA_CHAT_ID"

// chat writes to a Bot on the server, the main Bot unless --bot names
// another, in its main chat unless --chat names another or --new starts one
// (issue #471), and prints its replies until it is idle again; without a
// message it prints the chat's latest messages.
func chat(ctx context.Context, e *env, fs *flag.FlagSet) error {
	text, err := e.text(fs, "text")
	if err != nil {
		return err
	}

	if t := strings.Join(fs.Args(), " "); text == "" {
		text = t
	}

	bot := str(fs, "bot")
	if (bot != "" || boolean(fs, "new")) && str(fs, "chat") != "" {
		return errors.New("--chat names a chat, whose Bot is fixed; --bot and --new pick a Bot's main chat")
	}

	if strings.TrimSpace(text) == "" && !boolean(fs, "new") && str(fs, "chat") == "" {
		// Reading makes no chat: a Bot with no main chat has nothing to show.
		id, err := mainChatOf(ctx, e, bot)
		if err != nil {
			return err
		}

		if id == "" {
			e.printf("the bot has no main chat yet; `hypomnema chat <message>` starts it\n")

			return nil
		}

		c, err := pickChat(ctx, e, id, false, "", "")
		if err != nil {
			return err
		}

		return showChat(ctx, e, c, number(fs, "last"))
	}

	c, err := pickChat(ctx, e, str(fs, "chat"), boolean(fs, "new"), bot, chatTitle(text))
	if err != nil {
		return err
	}

	if strings.TrimSpace(text) == "" {
		return showChat(ctx, e, c, number(fs, "last"))
	}

	if !c.Coordinator {
		e.printf("note: no coordinator runs on the server; the message is kept and nothing answers\n")
	}

	sent, err := e.api.SendChatMessageWithResponse(ctx, c.Id, openapi.NewChatMessage{Text: text})
	if err != nil {
		return fmt.Errorf("sending the message: %w", err)
	}

	if sent.JSON202 != nil {
		// pi works on the chat: the message waits in its queue until the turn settles (issue #497).
		e.printf("chat %s\nqueued: the coordinator is still working; it reads this when its turn ends\n", c.Id)

		return nil
	}

	if sent.JSON201 == nil {
		return apiError("sending the message", sent.StatusCode(), sent.JSONDefault)
	}

	e.printf("chat %s\n", c.Id)

	if !c.Coordinator {
		return nil
	}

	return followChat(ctx, e, c.Id, sent.JSON201.Seq, durationFlag(fs, "timeout"))
}

// pickChat returns the chat named, a new main chat of bot (the main Bot when
// empty) titled title, or the Bot's main chat, made when it has none.
func pickChat(ctx context.Context, e *env, id string, fresh bool, bot, title string) (openapi.Chat, error) {
	if id != "" {
		r, err := e.api.GetChatWithResponse(ctx, id)
		if err != nil {
			return openapi.Chat{}, fmt.Errorf("reading chat %s: %w", id, err)
		}

		if r.JSON200 == nil {
			return openapi.Chat{}, apiError("reading chat "+id, r.StatusCode(), r.JSONDefault)
		}

		return *r.JSON200, nil
	}

	if !fresh {
		body := openapi.MainChatRequest{}
		if bot != "" {
			body.Bot = &bot
		}

		r, err := e.api.OpenMainChatWithResponse(ctx, body)
		if err != nil {
			return openapi.Chat{}, fmt.Errorf("opening the bot's main chat: %w", err)
		}

		switch {
		case r.JSON200 != nil:
			return *r.JSON200, nil
		case r.JSON201 != nil:
			return *r.JSON201, nil
		default:
			return openapi.Chat{}, apiError("opening the bot's main chat", r.StatusCode(), r.JSONDefault)
		}
	}

	newChat := openapi.NewChat{}
	if title != "" {
		newChat.Title = &title
	}

	if bot != "" {
		newChat.Bot = &bot
	}

	r, err := e.api.CreateChatWithResponse(ctx, newChat)
	if err != nil {
		return openapi.Chat{}, fmt.Errorf("starting a chat: %w", err)
	}

	if r.JSON201 == nil {
		return openapi.Chat{}, apiError("starting a chat", r.StatusCode(), r.JSONDefault)
	}

	return *r.JSON201, nil
}

// mainChatOf is the id of a Bot's main chat, the main Bot's when bot is empty; empty when it has none.
func mainChatOf(ctx context.Context, e *env, bot string) (string, error) {
	if bot != "" {
		r, err := e.api.GetBotWithResponse(ctx, bot)
		if err != nil {
			return "", fmt.Errorf("reading bot %s: %w", bot, err)
		}

		if r.JSON200 == nil {
			return "", apiError("reading bot "+bot, r.StatusCode(), r.JSONDefault)
		}

		return val(r.JSON200.MainChat), nil
	}

	r, err := e.api.ListBotsWithResponse(ctx)
	if err != nil {
		return "", fmt.Errorf("listing bots: %w", err)
	}

	if r.JSON200 == nil {
		return "", apiError("listing bots", r.StatusCode(), r.JSONDefault)
	}

	for _, b := range r.JSON200.Data {
		if b.Main {
			return val(b.MainChat), nil
		}
	}

	return "", errors.New("the server names no main bot")
}

// chatTitle is a message's first line, at most 80 characters, as the web titles a new chat.
func chatTitle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > 80 {
		line = string(r[:80])
	}

	return strings.TrimSpace(line)
}

// followChat prints the messages after the owner's until pi has taken it,
// answered, and is idle, or the timeout passes.
func followChat(ctx context.Context, e *env, chatID string, after int64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	answered := false

	for time.Now().Before(deadline) {
		wait := 20
		if answered {
			wait = 3 // only the end of the turn is left to see
		}

		r, err := e.api.ListChatMessagesWithResponse(ctx, chatID, &openapi.ListChatMessagesParams{
			After: &after, Wait: &wait,
		})
		if err != nil {
			return fmt.Errorf("reading the chat: %w", err)
		}

		if r.JSON200 == nil {
			return apiError("reading the chat", r.StatusCode(), r.JSONDefault)
		}

		for _, m := range r.JSON200.Data {
			printChatMessage(e, m)

			answered = answered || m.Role == openapi.ChatRoleCoordinator
		}

		after = r.JSON200.NextAfter

		if answered {
			c, err := e.api.GetChatWithResponse(ctx, chatID)
			if err == nil && c.JSON200 != nil && !c.JSON200.Working {
				return nil
			}
		}
	}

	e.printf("(still working; `hypomnema chat --chat %s` shows the rest later)\n", chatID)

	return nil
}

func showChat(ctx context.Context, e *env, c openapi.Chat, last int) error {
	var (
		after int64
		all   []openapi.ChatMessage
	)

	for {
		r, err := e.api.ListChatMessagesWithResponse(ctx, c.Id, &openapi.ListChatMessagesParams{After: &after})
		if err != nil {
			return fmt.Errorf("reading the chat: %w", err)
		}

		if r.JSON200 == nil {
			return apiError("reading the chat", r.StatusCode(), r.JSONDefault)
		}

		if len(r.JSON200.Data) == 0 {
			break
		}

		all = append(all, r.JSON200.Data...)
		after = r.JSON200.NextAfter
	}

	e.printf("chat %s: %s\n", c.Id, c.Title)

	for _, m := range all[max(0, len(all)-last):] {
		printChatMessage(e, m)
	}

	return nil
}

func printChatMessage(e *env, m openapi.ChatMessage) {
	switch m.Role {
	case openapi.ChatRoleCoordinator:
		if strings.TrimSpace(m.Text) == "" {
			return // only thinking, which the web shows
		}

		e.printf("\npi: %s\n", m.Text)
	case openapi.ChatRoleOwner:
		e.printf("\nyou: %s\n", m.Text)

		// The files the owner attached in the web (issue #343), by their place in the Bot's folder.
		for _, a := range val(m.Attachments) {
			e.printf("  attached: %s\n", a.Path)
		}
	default:
		e.printf("  · %s\n", m.Text)
	}
}

func chatsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	params := openapi.ListChatsParams{}
	if bot := str(fs, "bot"); bot != "" {
		params.Bot = &bot
	}

	r, err := e.api.ListChatsWithResponse(ctx, &params)
	if err != nil {
		return fmt.Errorf("listing chats: %w", err)
	}

	if r.JSON200 == nil {
		return apiError("listing chats", r.StatusCode(), r.JSONDefault)
	}

	return e.print(fs, r.JSON200, func() {
		for _, c := range r.JSON200.Data {
			state := "idle"
			if c.Working {
				state = "working"
			}

			e.printf("%s\t%s\t%s\t%s\n", c.Id, state, c.UpdatedAt.Local().Format("2006-01-02 15:04"), c.Title)
		}
	})
}

// chatsRemove deletes a chat (issue #484), as `opencode session delete` does. The chat stays deleted when some of
// its files could not be removed; each is named after it.
func chatsRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.DeleteChatWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting chat %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("deleting chat "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		e.printf("chat %s is deleted\n", id)

		for _, left := range resp.JSON200.FilesLeft {
			e.printf("a file of it stayed: %s\n", left)
		}
	})
}

func durationFlag(fs *flag.FlagSet, name string) time.Duration {
	d, _ := time.ParseDuration(str(fs, name))

	return d
}

// chatOfThisAgent is the chat pi works for, from its environment.
func chatOfThisAgent() *string {
	if id := os.Getenv(chatEnv); id != "" {
		return &id
	}

	return nil
}
