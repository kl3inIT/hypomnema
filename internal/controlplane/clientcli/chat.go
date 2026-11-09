package clientcli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// chatEnv names the chat pi works for; pi's environment carries it, so the
// Runs pi creates wake that chat (issue #41, PR 3).
const chatEnv = "HYPOMNEMA_CHAT_ID"

// chat writes to the coordinator on the server and prints its replies until
// it is idle again; without a message it prints the chat's latest messages.
func chat(ctx context.Context, e *env, fs *flag.FlagSet) error {
	text, err := e.text(fs, "text")
	if err != nil {
		return err
	}

	if t := strings.Join(fs.Args(), " "); text == "" {
		text = t
	}

	c, err := pickChat(ctx, e, str(fs, "chat"), boolean(fs, "new"))
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

	if sent.JSON201 == nil {
		return apiError("sending the message", sent.StatusCode(), sent.JSONDefault)
	}

	e.printf("chat %s\n", c.Id)

	if !c.Coordinator {
		return nil
	}

	return followChat(ctx, e, c.Id, sent.JSON201.Seq, durationFlag(fs, "timeout"))
}

// pickChat returns the chat named, a new one, or the most recent one.
func pickChat(ctx context.Context, e *env, id string, fresh bool) (openapi.Chat, error) {
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
		limit := 1

		r, err := e.api.ListChatsWithResponse(ctx, &openapi.ListChatsParams{Limit: &limit})
		if err != nil {
			return openapi.Chat{}, fmt.Errorf("listing chats: %w", err)
		}

		if r.JSON200 == nil {
			return openapi.Chat{}, apiError("listing chats", r.StatusCode(), r.JSONDefault)
		}

		if len(r.JSON200.Data) > 0 {
			return r.JSON200.Data[0], nil
		}
	}

	r, err := e.api.CreateChatWithResponse(ctx, openapi.NewChat{})
	if err != nil {
		return openapi.Chat{}, fmt.Errorf("starting a chat: %w", err)
	}

	if r.JSON201 == nil {
		return openapi.Chat{}, apiError("starting a chat", r.StatusCode(), r.JSONDefault)
	}

	return *r.JSON201, nil
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
		e.printf("\npi: %s\n", m.Text)
	case openapi.ChatRoleOwner:
		e.printf("\nyou: %s\n", m.Text)
	default:
		e.printf("  · %s\n", m.Text)
	}
}

func chatsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	r, err := e.api.ListChatsWithResponse(ctx, &openapi.ListChatsParams{})
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
